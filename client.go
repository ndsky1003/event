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
	"time"

	"github.com/ndsky1003/event/codec"
	"github.com/ndsky1003/event/msg"
	"github.com/ndsky1003/event/options"
	"github.com/sirupsen/logrus"
)

type ServerError string

func (this ServerError) Error() string {
	return string(this)
}

// 出现这个错的话就要尝试重新建立连接
var errLocalWrite = errors.New("local Write err")

type Client struct {
	name          string
	url           string
	writeMutex    sync.Mutex   // 保证流的正确性
	mutex         sync.RWMutex // 保护client的状态
	codecFunc     options.CreateClientCodecFunc
	codec         codec.Codec
	events        map[*msg.EventTopic][]*method
	seq           uint64
	pending       map[uint64]*msg.Call
	checkInterval time.Duration //链接检测
	heartInterval time.Duration //心跳间隔
	isStopHeart   bool          //是否关闭心跳
	connecting    bool          // client is connecting
}

func Dial(url string, opts ...*options.ClientOptions) *Client {
	c := &Client{
		url:           url,
		events:        make(map[*msg.EventTopic][]*method),
		pending:       make(map[uint64]*msg.Call),
		checkInterval: 1,
		heartInterval: 5,
	}
	//合并属性
	opt := options.Client().SetCodecFunc(func(conn io.ReadWriteCloser) (codec.Codec, error) {
		return codec.NewGobCodec(conn), nil
	}).Merge(opts...)

	//属性设置开始
	if opt.Name != nil {
		c.name = *opt.Name
	}
	if opt.CodecFunc != nil {
		c.codecFunc = *opt.CodecFunc
	}

	if opt.CheckInterval != nil {
		c.checkInterval = *opt.CheckInterval
	}

	if opt.HeartInterval != nil {
		c.heartInterval = *opt.HeartInterval
	}

	if opt.IsStopHeart != nil {
		c.isStopHeart = *opt.IsStopHeart
	}
	go c.keepAlive()
	return c
}

func (this *Client) getConnecting() bool {
	this.mutex.Lock()
	defer this.mutex.Unlock()
	return this.connecting
}

func (this *Client) keepAlive() {
	for {
		if !this.getConnecting() {
			conn, err := net.Dial("tcp", this.url)
			if err != nil {
				logrus.Errorf("dail err:%v\n", err)
				time.Sleep(this.checkInterval * time.Second)
				continue
			}
			codec, err := this.codecFunc(conn)
			if err != nil {
				logrus.Errorf("codec err:%v\n", err)
				time.Sleep(this.checkInterval * time.Second)
				continue
			} else {
				if err := this.serve(codec); err != nil {
					logrus.Error("server:", err)
				}
				time.Sleep(this.checkInterval * time.Second) //下次去尝试连接
				continue
			}
		} else { //heart
			if !this.isStopHeart {
				if call := this.emit_async(msg.MsgType_ping, ""); call != nil {
					err := call.Error
					if err != nil { //这里是同步触发的错误
						logrus.Error(err)
						if errors.Is(err, io.ErrShortWrite) || errors.Is(err, errLocalWrite) {
							this.stop(err)
						}
					}
				}
				time.Sleep(this.heartInterval * time.Second)
			} else {
				time.Sleep(this.checkInterval * time.Second) //下次去尝试连接
			}
		}
	}
}

func (this *Client) serve(codec codec.Codec) (err error) {
	this.mutex.Lock()
	defer func() {
		if err != nil {
			this.mutex.Unlock()
		}
	}()
	if err = codec.Write(&msg.Msg{T: msg.MsgType_varify, Name: this.name}); err != nil {
		return
	}
	var readFirstMsg msg.Msg
	if err = codec.Read(&readFirstMsg); err != nil {
		return
	}
	if readFirstMsg.T == msg.MsgType_prepared {
		//重连挂载已经有的event
		for tp := range this.events {
			if err = codec.Write(&msg.Msg{T: msg.MsgType_on, EventType: tp.GetEventType()}); err != nil {
				return
			}
		}
	}
	this.connecting = true
	this.codec = codec
	this.mutex.Unlock()
	go this.input(codec)
	return
}

func (this *Client) stop(err error) {
	this.mutex.Lock()
	defer this.mutex.Unlock()
	for _, call := range this.pending {
		call.Error = err
		logrus.Errorf("%+v,err:%v", call.Msg, call.Error)
		call.Do()
	}
	if this.connecting {
		this.codec.Close()
		this.codec = nil
	}
	this.seq = 0
	this.pending = make(map[uint64]*msg.Call)
	this.connecting = false
}

func (this *Client) StopHeart() {
	this.mutex.Lock()
	defer this.mutex.Unlock()
	this.isStopHeart = true

}

func (this *Client) PrintCall() {
	for index, msg := range this.pending {
		logrus.Infof("index:%d,msg:%+v\n", index, msg.Error)
	}
}

func (this *Client) input(codec codec.Codec) {
	var err error
	for err == nil {
		var gotMsg msg.Msg
		err = codec.Read(&gotMsg)
		if err != nil {
			err = errors.New("reading error body1: " + err.Error())
			break
		}
		switch gotMsg.T {
		case msg.MsgType_ping:
		case msg.MsgType_req:
			//fmt.Printf("client receive:%+v\n", gotMsg)
			go this.call(codec, &gotMsg)
		case msg.MsgType_res, msg.MsgType_on, msg.MsgType_pong:
			if gotMsg.T != msg.MsgType_pong {
				//fmt.Printf("client receive:%+v\n", gotMsg)
			}
			seq := gotMsg.ClientSeq
			this.mutex.Lock()
			call := this.pending[seq]
			delete(this.pending, seq)
			this.mutex.Unlock()
			if call != nil {
				if gotMsg.Error != "" {
					call.Error = ServerError(gotMsg.Error)
				}
				call.Do()
			}
		}
	}
	logrus.Errorf("read err:%+v", err)
	this.stop(err)
}

func (this *Client) parse(data []byte, argCount int, m *method) []reflect.Value {
	argsValue := make([]reflect.Value, m.argCount)
	var dstData = make([]byte, len(data))
	copy(dstData, data)
	dec := gob.NewDecoder(bytes.NewReader(dstData))
	for i := 0; i < m.argCount; i++ {
		argType := m.argsType[i]
		at := argType.at
		if argType.isPointer {
			at = at.Elem()
		}
		argValue := reflect.New(at)
		if i < argCount {
			if err := dec.Decode(argValue.Interface()); err != nil {
				logrus.Error(err)
			}
		}
		if !argType.isPointer {
			argValue = argValue.Elem()
		}
		argsValue[i] = argValue
	}
	return argsValue
}

func (this *Client) call(codec codec.Codec, req *msg.Msg) {
	et := req.EventType
	res := &msg.Msg{
		T:         msg.MsgType_res,
		ServerSeq: req.ServerSeq,
		ClientSeq: req.ClientSeq,
		EventType: et,
	}
	var err error
	this.mutex.RLock()
	var isHasFunc bool
	var doMethods = map[*msg.EventTopic]*method{}
	for tp, funcs := range this.events {
		if tp.Match(et) {
			for _, method := range funcs {
				isHasFunc = true
				doMethods[tp] = method
			}
		}
	}
	this.mutex.RUnlock()
	if !isHasFunc {
		err = fmt.Errorf("has no func to do:%v", req.EventType)
	} else {
		for tp, method := range doMethods {
			args := this.parse(req.Bytes, int(req.BodyCount), method)
			if tp.IsRegexp {
				args = append([]reflect.Value{reflect.ValueOf(tp.FindStringSubmatch(et))}, args...)
			}
			returnValues := method.function.Call(args)
			errInter := returnValues[0].Interface()
			if errInter != nil {
				appendErr := errInter.(error)
				appendErr = fmt.Errorf("[topic:%v,event:%s,err:%v]", tp.GetEventType(), et, appendErr)
				if err != nil {
					err = fmt.Errorf("%w,%w", err, appendErr)
				} else {
					err = appendErr
				}
			}
		}
		if err != nil {
			res.Error = err.Error()
		}
	}
	this.writeMutex.Lock()
	err = codec.Write(res)
	this.writeMutex.Unlock()
	if err != nil {
		this.stop(err)
	}
}

var errType = reflect.TypeOf((*error)(nil)).Elem()
var sliceType = reflect.TypeOf((*[]string)(nil)).Elem()

// 监听事件
// if t is reg , first param must the value of reg.FindStringSubmatch(et)
func (this *Client) On(t msg.EventType, Func any) error {
	if t == "" {
		return errors.New("event type must not empty")
	}
	rt := reflect.TypeOf(Func)
	if rt.Kind() != reflect.Func {
		panic("on a not func")
	}
	if rt.NumOut() != 1 {
		return errors.New("must has 1 return value")
	}
	if rt.Out(0) != errType {
		return errors.New("return param must error")
	}
	inCount := rt.NumIn()
	length := inCount
	newtp := msg.NewEventTopic(t)
	var inStart int
	if newtp.IsRegexp {
		if inCount < 1 {
			return errors.New("parm must have one and is a []string")
		}
		if rt.In(0) != sliceType {
			return errors.New("On Action first param must []string")
		}
		inStart = 1
		inCount = inCount - 1
	}

	var argsType = make([]*argType, 0, inCount)
	for i := inStart; i < length; i++ {
		at := rt.In(i)
		argsType = append(argsType, &argType{isPointer: at.Kind() == reflect.Pointer, at: at})
	}
	rv := reflect.ValueOf(Func)
	mType := &method{
		function: rv,
		argsType: argsType,
		argCount: inCount,
	}
	this.mutex.Lock()
	var isExist bool
	var methods []*method
	var topic *msg.EventTopic
	for tp, ev := range this.events {
		if tp.Equal(t) {
			methods = append(ev, mType)
			topic = tp
			isExist = true
			break
		}
	}
	if isExist {
		this.events[topic] = methods
	} else {
		methods = []*method{mType}
		this.events[msg.NewEventTopic(t)] = methods
	}
	this.mutex.Unlock()
	for { //强制成功,因为有些时候,程序启动,事件需要立马注册,有可能链接未完成
		if err := this.emit(msg.MsgType_on, t); errors.Is(err, ErrNoConnect) {
			logrus.Errorf("err:%+v", err)
			time.Sleep(2e9)
			continue
		}
		break
	}
	return nil
}

func (this *Client) EmitAsync(eventType msg.EventType, args ...any) (call *msg.Call) {
	return this.emit_async(msg.MsgType_req, eventType, args...)
}

func (this *Client) emit_async(t msg.MsgType, eventType msg.EventType, args ...any) (call *msg.Call) {
	m := &msg.Msg{
		T:         t,
		EventType: eventType,
		BodyCount: int8(len(args)),
	}
	call = msg.NewCall(m)
	var buf bytes.Buffer
	paramEncoder := gob.NewEncoder(&buf)
	var err error
	for _, arg := range args {
		if err = paramEncoder.Encode(arg); err != nil {
			err = fmt.Errorf("current:%v,err:%w", err, errLocalWrite)
			break
		}
	}
	if err != nil {
		call.Error = err
		call.Do()
		return
	}
	m.Bytes = buf.Bytes()
	this.send(call)
	return
}

func (this *Client) emit(t msg.MsgType, eventType msg.EventType, args ...any) error {
	call := <-this.emit_async(t, eventType, args...).Done
	return call.Error
}

// 触发事件
func (this *Client) Emit(eventType msg.EventType, args ...any) error {
	return this.emit(msg.MsgType_req, eventType, args...)
}

func (this *Client) send(call *msg.Call) {
	var codec codec.Codec
	this.mutex.Lock()
	if !this.connecting {
		this.mutex.Unlock()
		call.Error = fmt.Errorf("client is connecting:%v,%w", this.connecting, ErrNoConnect)
		call.Do()
		return
	}
	codec = this.codec
	seq := this.seq
	seq = incSeqID(seq)
	this.pending[seq] = call
	this.seq = seq
	this.mutex.Unlock()
	call.Msg.ClientSeq = seq
	this.writeMutex.Lock()
	err := codec.Write(call.Msg)
	this.writeMutex.Unlock()
	if err != nil {
		this.mutex.Lock()
		call = this.pending[seq]
		delete(this.pending, seq)
		this.mutex.Unlock()
		if call != nil {
			err = fmt.Errorf("current:%v,err:%w", err, errLocalWrite)
			call.Error = err
			call.Do()
		}
	}
}

func (this *Client) Seq() uint64 {
	return this.seq
}
