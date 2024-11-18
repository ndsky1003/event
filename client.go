package event

import (
	"bytes"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ndsky1003/buffer"
	"github.com/ndsky1003/event/v2/codec"
	"github.com/ndsky1003/event/v2/eventname"
	"github.com/ndsky1003/event/v2/msg"
	"github.com/ndsky1003/event/v2/msgtype"
	"github.com/ndsky1003/event/v2/topic"
	"github.com/sirupsen/logrus"
)

func init() {
	logrus.SetReportCaller(true)
}

type Client struct {
	url       string
	seq       uint64
	opt       *ClientOption
	codecFunc codec.CreateCodecFunc

	rwl    sync.RWMutex // protect under ,这个显然是读大于写,写只有on的时候用
	topics map[*topic.Topic][]*method

	sync.Mutex // protect under
	codec      codec.Codec
	pending    map[uint64]*Call
	connecting bool // client is connecting
}

func Dial(url string, opts ...*ClientOption) *Client {
	c := &Client{
		url:     url,
		topics:  make(map[*topic.Topic][]*method),
		pending: make(map[uint64]*Call),
		codecFunc: func(conn io.ReadWriteCloser) (codec.Codec, error) {
			return codec.NewGobCodec(conn), nil
		},
	}
	c.opt = ClientOptions().
		SetName("").
		SetCheckInterval(2).
		SetHeartInterval(20).
		SetSecret("").
		SetIsWrapError(true).
		merges(opts...)
	go c.keepAlive()
	return c
}

func (this *Client) getConnecting() bool {
	this.Lock()
	defer this.Unlock()
	return this.connecting
}

func (this *Client) keepAlive() {
	heat_interval := *this.opt.heart_interval
	for {
		if !this.getConnecting() {
			conn, err := net.Dial("tcp", this.url)
			if err != nil {
				err = fmt.Errorf("%w Dail err:%w", ErrClient, err)
				logrus.Error(err)
				time.Sleep(*this.opt.check_interval * time.Second)
				continue
			}
			codec, err := this.codecFunc(conn)
			if err != nil {
				err = fmt.Errorf("%w newcodec err:%w", ErrClient, err)
				logrus.Error(err)
				time.Sleep(*this.opt.check_interval * time.Second)
				continue
			} else {
				if err := this.serve(codec); err != nil {
					err = fmt.Errorf("%w serve err:%w", ErrClient, err)
					logrus.Error(err)
				}
				time.Sleep(*this.opt.check_interval * time.Second) //下次去尝试连接
				continue
			}
		} else {
			if heat_interval > 0 {
				if call := this.emit_async(msgtype.Ping, ""); call != nil {
					err := call.Err
					if err != nil { //这里是同步触发的错误
						logrus.Error(err)
						this.Stop(err)
					}
				}
				time.Sleep(heat_interval * time.Second)
			} else {
				time.Sleep(*this.opt.check_interval * time.Second) //下次去尝试连接
			}
		}
	}
}

func (this *Client) serve(codec codec.Codec) (err error) {
	this.Lock()
	defer func() {
		if err != nil {
			this.Unlock()
		}
	}()
	if err = codec.Write(&msg.MsgVerifyReq{Name: *this.opt.name, Secret: *this.opt.secret}); err != nil {
		return
	}
	var readFirstMsg msg.MsgVerifyRes
	if err = codec.Read(&readFirstMsg); err != nil {
		return
	}

	if readFirstMsg.Err != "" {
		err = errors.New(readFirstMsg.Err)
		return
	}
	this.connecting = true
	this.codec = codec
	this.Unlock()
	go this.input(codec)
	return
}

func (this *Client) Stop(err error) {
	this.Lock()
	defer this.Unlock()
	this.stop(err)
}

func (this *Client) stop(err error) {
	for _, call := range this.pending {
		call.Err = err
		logrus.Errorf("%+v,err:%v", call.Msg, call.Err)
		call.done()
	}

	this.rwl.Lock()
	for tp := range this.topics {
		tp.IsRegistSuccess = false
	}
	this.rwl.Unlock()

	if this.codec != nil {
		this.codec.Close()
		this.codec = nil
	}
	this.seq = 0
	this.pending = make(map[uint64]*Call)
	this.connecting = false
}

func (this *Client) input(codec codec.Codec) {
	go this.regist_topic()
	var err error
	for err == nil {
		var gotMsg msg.Msg
		err = codec.Read(&gotMsg)
		if err != nil {
			err = fmt.Errorf("%w,read body err:%w", ErrClientDecode, err)
			break
		}
		switch gotMsg.T {
		case msgtype.Ping:
		case msgtype.Req, msgtype.ReqSomeOne:
			go this.func_call(&gotMsg)
		case msgtype.Res, msgtype.ResSomeOne, msgtype.On, msgtype.Pong:
			seq := gotMsg.Seq
			this.Lock()
			call := this.pending[seq]
			delete(this.pending, seq)
			this.Unlock()
			if call != nil {
				if gotMsg.Err != "" {
					var err error
					if *this.opt.is_wrap_error {
						err = fmt.Errorf("%w,%v", ErrServer, gotMsg.Err)
					} else {
						err = errors.New(gotMsg.Err)
					}
					call.Err = err
				}
				call.done()
			}
		}
	}
	logrus.Error(err)
	this.Stop(err)
}

func (this *Client) parse(req_body_data []byte, req_args_count int, m *method) (argsValue []reflect.Value, err error) {
	argsValue = make([]reflect.Value, m.argsCount)
	// var dstData = make([]byte, len(req_body_data))
	// copy(dstData, req_body_data)
	dec := gob.NewDecoder(bytes.NewReader(req_body_data))
	for i := 0; i < m.argsCount; i++ {
		argType := m.argsType[i]
		at := argType.at
		if argType.isPointer {
			at = at.Elem()
		}
		argValue := reflect.New(at)
		if i < req_args_count {
			if err = dec.Decode(argValue.Interface()); err != nil {
				ft := m.function.Type()
				err = fmt.Errorf("%w,parse func[%v] body [%v] arg err:%w", ErrClient, ft, i, err)
				return
			}
		}
		if !argType.isPointer {
			argValue = argValue.Elem()
		}
		argsValue[i] = argValue
	}
	return
}

func (this *Client) func_call(req *msg.Msg) {
	et := req.EventName
	t := msgtype.Res
	if req.T == msgtype.ReqSomeOne {
		t = msgtype.ResSomeOne
	}
	res := &msg.Msg{
		T:         t,
		Seq:       req.Seq,
		EventName: et,
	}
	this.rwl.RLock()
	var isHaveTP bool
	topics_tmp := map[*topic.Topic][]*method{}
	for tp, funcs := range this.topics {
		if tp.Match(et) {
			topics_tmp[tp] = funcs
			isHaveTP = true
		}
	}
	this.rwl.RUnlock()
	if isHaveTP {
		var errs []error
		for tp, methods := range topics_tmp {
			for _, method := range methods {
				args, err := this.parse(req.Bytes, int(req.BodyCount), method)
				if err != nil {
					errs = append(errs, err)
					continue
				}
				if tp.IsReg {
					args = append([]reflect.Value{reflect.ValueOf(tp.FindStringSubmatch(et))}, args...)
				}
				returnValues := method.function.Call(args)
				err_value := returnValues[0]
				if err_value.IsValid() && !err_value.IsNil() {
					appendErr := err_value.Interface().(error)
					if *this.opt.is_wrap_error {
						appendErr = fmt.Errorf("[client:%v,topic:%v,event:%s,err:%w]", *this.opt.name, tp.GetEventName(), et, appendErr)
					}
					errs = append(errs, appendErr)
				}
			}
		}
		if len(errs) > 0 {
			res.Err = errors.Join(errs...).Error()
		}
	}

	if err := this.Write(res); err != nil {
		logrus.Error(err)
	}
}

func (this *Client) Write(msg *msg.Msg) error {
	this.Lock()
	defer this.Unlock()
	if err := this.write(msg); err != nil {
		this.stop(err)
		return err
	}
	return nil
}

func (this *Client) write(msg *msg.Msg) error {
	if codec := this.codec; codec != nil {
		return codec.Write(msg)
	}
	return ErrClientCodecNil
}

func (this *Client) regist_topic() {
	for {
		if err := this.regist_topic_lock(); err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		return
	}
}

func (this *Client) regist_topic_lock() error {
	var errs []error
	this.rwl.Lock()
	for tp := range this.topics {
		if !tp.IsRegistSuccess {
			if err := this.emit(msgtype.On, tp.GetEventName()); err != nil {
				err := fmt.Errorf("%w, emit_on:[%v] err:%w", ErrClient, tp.GetEventName(), err)
				logrus.Error(err)
				errs = append(errs, err)
			} else {
				tp.IsRegistSuccess = true
			}
		}
	}
	this.rwl.Unlock()
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (this *Client) emit_async(t msgtype.T, en eventname.T, args ...any) (call *Call) {
	m := &msg.Msg{
		T:         t,
		EventName: en,
		BodyCount: int8(len(args)),
	}
	call = NewCall(m)
	if m.EventName == "" {
		call.Err = fmt.Errorf("%w,%v", ErrServer, "event name empty")
		call.done()
	}
	if len(args) > 0 {
		buf := buffer.Get()
		defer buf.Release()
		paramEncoder := gob.NewEncoder(buf)
		var err error
		for _, arg := range args {
			if err = paramEncoder.Encode(arg); err != nil {
				err = fmt.Errorf("%w %w current err:%w", ErrClient, ErrClientEncode, err)
				break
			}
		}
		if err != nil {
			call.Err = err
			call.done()
			return
		}
		m.Bytes = buf.Bytes()
	}
	this.send(call)
	return
}

func (this *Client) emit(t msgtype.T, en eventname.T, args ...any) error {
	call := <-this.emit_async(t, en, args...).Done
	return call.Err
}

func (this *Client) send(call *Call) {
	seq := atomic.AddUint64(&this.seq, 1)
	var err error
	this.Lock()
	defer this.Unlock()
	this.pending[seq] = call
	call.Msg.Seq = seq
	if b := this.connecting; !b {
		err = fmt.Errorf("%w %w connecting:%v", ErrClient, ErrNoConnect, b)
	}
	if err == nil {
		err = this.write(call.Msg) //this.codec.Write(call.Msg)
	}
	if err != nil {
		delete(this.pending, seq)
		call.Err = err
		call.done()
	}
}
