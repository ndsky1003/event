package event

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ndsky1003/event/v2/coder"
	"github.com/ndsky1003/event/v2/eventname"
	"github.com/ndsky1003/event/v2/msg"
	"github.com/ndsky1003/event/v2/msgtype"
	"github.com/sirupsen/logrus"
)

var bodyPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 1024)
		return b
	},
}

// 锁顺序固定为 rwl -> l。任何持 l 的代码路径都不得再去拿 rwl，
// 否则会和 On / regist_topic_lock / Stop 交叉死锁（原代码 FIXME 承认的那个问题）。
type Client struct {
	url    string
	seq    atomic.Uint64
	opt    *ClientOption
	closed atomic.Bool
	ctx    context.Context
	cancel context.CancelFunc

	rwl    sync.RWMutex // protect topics，只有 on 的时候写
	topics []topicEntry

	l       sync.Mutex // protect cc / pending / connecting / stopped
	conn    *Conn
	pending map[uint64]*Call
}

func Dial(url string, opts ...*ClientOption) *Client {
	c := &Client{
		url:     url,
		pending: make(map[uint64]*Call),
	}
	c.opt = ClientOptions().
		SetName("").
		SetCheckInterval(2 * time.Second).
		SetHeartInterval(10 * time.Second).
		SetSecret("").
		SetIsWrapError(true).
		SetCoderType(coder.Msgp).
		merges(opts...)
	c.ctx, c.cancel = context.WithCancel(context.TODO())

	go c.keepAlive()
	return c
}

// keepAlive 拨号 → 握手 → 跑连接 → 断了再连。
// 心跳与断连检测已经下沉到 Conn 的看门狗，这里不再发应用层 Ping。
// Close 之后退出，不再重连。
func (this *Client) keepAlive() {
	for {
		select {
		case <-this.ctx.Done():
			return
		default:
		}
		conn, err := this.connect(this.ctx)
		if conn != nil {
			this.disconnect(conn)
		}
		if err != nil {
			slog.Error("keepAlive", "err", err)
		}
		time.Sleep(*this.opt.check_interval) //下次去尝试连接
	}
}

// connect 建立一条连接并阻塞到它结束，返回连接终止的原因
func (this *Client) connect(ctx context.Context) (conn *Conn, err error) {
	raw, err := net.Dial("tcp", this.url)
	if err != nil {
		err = fmt.Errorf("%w Dail err:%w", ErrClient, err)
		return
	}
	conn = New(ctx, raw, this, &this.opt.connOption)
	if err = this.handshake(conn); err != nil {
		return
	}
	this.SetConn(conn)
	err = conn.Serve()
	return
}

// handshake 在 pump 启动前完成鉴权。必须单 goroutine 调用。
func (this *Client) handshake(conn *Conn) error {
	req := &msg.MsgVerifyReq{
		Name:   *this.opt.name,
		Secret: *this.opt.secret,
	}
	body, err := coder.Encode(coder.Msgp, nil, req)
	if err != nil {
		return fmt.Errorf("%w,encode verify req err:%w", ErrClient, err)
	}
	if err = conn.WriteToConn(5*time.Second, body); err != nil {
		return fmt.Errorf("%w,write verify req err:%w", ErrClient, err)
	}

	body, err = conn.Read(5 * time.Second)
	if err != nil {
		return fmt.Errorf("%w,read verify res err:%w", ErrClient, err)
	}
	var res msg.MsgVerifyRes
	if _, err = coder.Decode(coder.Msgp, body, &res); err != nil {
		return fmt.Errorf("%w,decode verify res err:%w", ErrClient, err)
	}
	if res.Err != "" {
		return fmt.Errorf("%w,%v", ErrServer, res.Err)
	}
	return nil
}

// HandleMsg 实现 Handler，由这条连接的 readPump 同步调用。
// PING/PONG 已在传输层消化，不会走到这里。
func (this *Client) HandleMsg(body []byte) error {
	var got msg.Msg
	if _, err := coder.Decode(coder.Msgp, body, &got); err != nil {
		return fmt.Errorf("%w,read body err:%w", ErrClientDecode, err)
	}
	switch got.T {
	case msgtype.Req, msgtype.ReqRandOne:
		go this.func_call(&got)
	case msgtype.Res, msgtype.ResRandOne, msgtype.On:
		this.resolve(&got)
	default:
		logrus.Infof("%v,invalid msg:%+v", ErrClient, got)
	}
	return nil
}

// resolve 把响应结算到对应的 Call 上
func (this *Client) resolve(got *msg.Msg) {
	seq := got.Seq
	this.l.Lock()
	call := this.pending[seq]
	delete(this.pending, seq)
	this.l.Unlock()
	if call == nil {
		return
	}
	if got.Err != "" {
		var err error
		if *this.opt.is_wrap_error {
			err = fmt.Errorf("%w,%v", ErrServer, got.Err)
		} else {
			err = errors.New(got.Err)
		}
		call.Err = err
	}
	call.done()
}
func (this *Client) SetConn(conn *Conn) {
	this.rwl.Lock()
	defer this.rwl.Unlock()
	this.l.Lock()
	defer this.l.Unlock()
	if c := this.conn; c != nil && c != conn {
		c.CloseWithError(nil)
	}
	this.conn = conn
	this.stopLocked(fmt.Errorf("%w,reset connection", ErrClient))
}

func (this *Client) disconnect(conn *Conn) {
	this.rwl.Lock()
	defer this.rwl.Unlock()
	this.l.Lock()
	defer this.l.Unlock()

	if c := this.conn; c != nil && c == conn {
		this.conn = nil
		this.stopLocked(fmt.Errorf("%w,connection lost", ErrClient))
	}
}

func (this *Client) Close() {
	this.CloseWithErr(fmt.Errorf("%w shutdown", ErrClient))
}

func (this *Client) CloseWithErr(err error) error {
	if !this.closed.CompareAndSwap(false, true) {
		return nil
	}
	this.rwl.Lock()
	defer this.rwl.Unlock()
	this.l.Lock()
	defer this.l.Unlock()

	this.cancel()
	if c := this.conn; c == nil {
		return nil
	}
	this.conn = nil
	this.stopLocked(fmt.Errorf("%w,%w client closed", ErrClient, err))
	return err
}

// stopLocked 必须持有 rwl 写锁与 l
func (this *Client) stopLocked(err error) {
	pending := this.pending
	for _, call := range pending {
		call.Err = err
		call.done()
	}
	this.pending = make(map[uint64]*Call)
	for i := range this.topics {
		this.topics[i].tp.IsRegistSuccess = false
	}
}

func (this *Client) parse(coderType coder.T, req_body_data []byte, req_args_count int, m *method) (argsValue []reflect.Value, err error) {
	argsValue = make([]reflect.Value, m.argsCount)
	rest := req_body_data
	for i := 0; i < m.argsCount; i++ {
		argType := m.argsType[i]
		argValue := reflect.New(argType.at)
		if i < req_args_count {
			if rest, err = coder.Decode(coderType, rest, argValue.Interface()); err != nil {
				ft := m.function.Type()
				err = fmt.Errorf("%w,parse func[%v] body [%v] arg err:%w", ErrClient, ft, i, err)
				return
			}
		}
		argsValue[i] = argValue.Elem()
	}
	return
}

func (this *Client) func_call(req *msg.Msg) {
	et := req.EventName
	t := msgtype.Res
	if req.T == msgtype.ReqRandOne {
		t = msgtype.ResRandOne
	}
	res := &msg.Msg{
		T:         t,
		Seq:       req.Seq,
		EventName: et,
	}
	this.rwl.RLock()
	var isHaveTP bool
	var topics_tmp []topicEntry
	for _, e := range this.topics {
		if e.tp.Match(et) {
			topics_tmp = append(topics_tmp, e)
			isHaveTP = true
		}
	}
	this.rwl.RUnlock()
	if isHaveTP {
		var errs []error
		for _, e := range topics_tmp {
			tp := e.tp
			for _, method := range e.methods {
				args, err := this.parse(req.Coder, req.Bytes, int(req.BodyCount), method)
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

// Write 编码并发送一条消息。失败只返回错误，连接收尾交给 readPump/Serve，
// 不在这里递归调用 Stop（旧实现里 Write→stop→Write 可能重入）。
func (this *Client) Write(m *msg.Msg) error {
	var c *Conn
	this.l.Lock()
	c = this.conn
	this.l.Unlock()
	if c == nil {
		return fmt.Errorf("%w client connection is nil", ErrClient)
	}
	body, err := coder.Encode(*this.opt.coder_type, nil, m)
	if err != nil {
		return fmt.Errorf("%w, err:%w", ErrClient, err)
	}
	return c.Write(body)

}

// regist_topic 把 On 注册的 topic 上报给服务端。
// 连接没起来或已断就直接返回——connect() 成功后会再拉一次，不必空转报错。
func (this *Client) regist_topic() {
	for {
		if err := this.regist_topic_lock(); err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		return
	}
}

// regist_topic_lock 锁顺序 rwl -> l（emit → send 会拿 l）
func (this *Client) regist_topic_lock() error {
	var errs []error
	this.rwl.Lock()
	for i := range this.topics {
		tp := this.topics[i].tp
		if !tp.IsRegistSuccess {
			if err := this.emit(msgtype.On, tp.GetEventName()); err != nil {
				err := fmt.Errorf("%w, emit_on:[%v] err:%w", ErrClient, tp.GetEventName(), err)
				slog.Error("client.go", "err", err)
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

func (this *Client) emit_async(t msgtype.T, en eventname.T, opt SendOption, args ...any) (call *Call) {
	m := &msg.Msg{
		T:         t,
		EventName: en,
		Coder:     *opt.ct,
	}
	call = NewCall(m)
	if m.EventName == "" {
		call.Err = fmt.Errorf("%w,%v", ErrServer, "event name empty")
		call.done()
		return
	}
	// BodyCount 是 int8。超过 127 个参数会溢出成负数，
	// 接收端 parse() 的 `i < req_args_count` 直接恒假，参数被静默丢掉。
	// 与其悄悄错，不如在这里报错。
	if len(args) > math.MaxInt8 {
		call.Err = fmt.Errorf("%w,too many args:%d,limit %d", ErrClient, len(args), math.MaxInt8)
		call.done()
		return
	}
	m.BodyCount = int8(len(args))
	if len(args) > 0 {
		body := bodyPool.Get().([]byte)[:0]
		var err error
		for _, arg := range args {
			if body, err = coder.Encode(m.Coder, body, arg); err != nil {
				err = fmt.Errorf("%w %w current err:%w", ErrClient, ErrClientEncode, err)
				break
			}
		}
		if err != nil {
			// coder.Encode 失败时返回 nil，别把 nil 塞回池子污染后面的 Get
			if body != nil {
				bodyPool.Put(body[:0])
			}
			call.Err = err
			call.done()
			return
		}
		m.Bytes = make([]byte, len(body))
		copy(m.Bytes, body)
		bodyPool.Put(body[:0])
	}
	this.send(call)
	return
}

func (this *Client) send(call *Call) (err error) {
	seq := this.seq.Add(1)
	call.Msg.Seq = seq
	defer func() {
		if err != nil {
			this.l.Lock()
			delete(this.pending, seq)
			this.l.Unlock()
			call.Err = err
			call.done()
		}
	}()
	this.l.Lock()
	this.pending[seq] = call
	this.l.Unlock()
	err = this.Write(call.Msg)
	return
}
