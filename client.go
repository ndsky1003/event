package event

import (
	"errors"
	"fmt"
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
	"github.com/ndsky1003/event/v2/topic"
	"github.com/sirupsen/logrus"
)

var bodyPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 1024)
		return b
	},
}

func init() {
	logrus.SetReportCaller(true)
}

// 锁顺序固定为 rwl -> l。任何持 l 的代码路径都不得再去拿 rwl，
// 否则会和 On / regist_topic_lock / Stop 交叉死锁（原代码 FIXME 承认的那个问题）。
type Client struct {
	url       string
	seq       uint64
	opt       *ClientOption
	coderType coder.T //事件参数的编码方式,可通过 ClientOption.SetCoderType 注入

	rwl    sync.RWMutex // protect topics，只有 on 的时候写
	topics map[*topic.Topic][]*method

	l          sync.Mutex // protect cc / pending / connecting / stopped
	cc         *clientConn
	pending    map[uint64]*Call
	connecting bool // client is connecting
	stopped    bool // Close 过了，keepAlive 不再重连
}

// clientConn 一次连接的上下文：传输层 Conn + 这条连接的消息框架编码方式。
// envCoder 由握手确定（客户端发起、服务端回包沿用），断线重连会重新协商。
type clientConn struct {
	conn     *Conn
	envCoder coder.T
	client   *Client
}

func Dial(url string, opts ...*ClientOption) *Client {
	c := &Client{
		url:       url,
		topics:    make(map[*topic.Topic][]*method),
		pending:   make(map[uint64]*Call),
		coderType: coder.Msgp,
	}
	c.opt = ClientOptions().
		SetName("").
		SetCheckInterval(2 * time.Second).
		SetHeartInterval(20 * time.Second).
		SetSecret("").
		SetIsWrapError(true).
		merges(opts...)
	if c.opt.coder_type != nil {
		c.coderType = *c.opt.coder_type
	}
	go c.keepAlive()
	return c
}

// connOptions 把客户端配置映射到传输层参数
func (this *ClientOption) connOptions() *connOption {
	o := ConnOptions()
	if this.heart_interval != nil {
		o.SetHeartInterval(*this.heart_interval)
	}
	return o
}

func (this *Client) isConnected() bool {
	this.l.Lock()
	defer this.l.Unlock()
	return this.connecting && this.cc != nil
}

// isStopped 是否已经 Close
func (this *Client) isStopped() bool {
	this.l.Lock()
	defer this.l.Unlock()
	return this.stopped
}

// keepAlive 拨号 → 握手 → 跑连接 → 断了再连。
// 心跳与断连检测已经下沉到 Conn 的看门狗，这里不再发应用层 Ping。
// Close 之后退出，不再重连。
func (this *Client) keepAlive() {
	for {
		if this.isStopped() {
			return
		}
		if err := this.connect(); err != nil {
			logrus.Error(err)
		}
		if this.isStopped() {
			return
		}
		time.Sleep(*this.opt.check_interval) //下次去尝试连接
	}
}

// connect 建立一条连接并阻塞到它结束，返回连接终止的原因
func (this *Client) connect() (err error) {
	raw, err := net.Dial("tcp", this.url)
	if err != nil {
		return fmt.Errorf("%w Dail err:%w", ErrClient, err)
	}
	cc := &clientConn{client: this, envCoder: envelopeCoder(this.coderType)}
	cc.conn, err = New(raw, cc, this.opt.connOptions())
	if err != nil {
		if e := raw.Close(); e != nil {
			err = fmt.Errorf("%w,%w", err, e)
		}
		return fmt.Errorf("%w new conn err:%w", ErrClient, err)
	}
	if err = cc.handshake(); err != nil {
		if e := cc.conn.Close(); e != nil {
			err = fmt.Errorf("%w,%w", err, e)
		}
		return err
	}

	this.l.Lock()
	if this.stopped {
		// Close 正好落在"拨号完成"和"发布 cc"之间。这条新连接不在 this.cc 里，
		// Close 关不到它；不在这儿关掉就会漏一条连接和一个卡在读上的 goroutine。
		this.l.Unlock()
		if e := cc.conn.Close(); e != nil {
			return fmt.Errorf("%w,%w", ErrClient, e)
		}
		return nil
	}
	this.cc = cc
	this.connecting = true
	this.l.Unlock()

	go this.regist_topic()

	err = cc.conn.Serve()
	this.disconnect(cc)
	switch {
	case err == nil, errors.Is(err, ErrConnClosed):
		// 主动 Close（Stop / Write 失败后自杀）不是异常，别刷错误日志
		return nil
	default:
		return fmt.Errorf("%w serve err:%w", ErrClient, err)
	}
}

// handshake 在 pump 启动前完成鉴权。必须单 goroutine 调用。
func (this *clientConn) handshake() error {
	c := this.client
	body, err := encodeEnvelope(this.envCoder, &msg.MsgVerifyReq{
		Name:   *c.opt.name,
		Secret: *c.opt.secret,
	})
	if err != nil {
		return fmt.Errorf("%w,encode verify req err:%w", ErrClient, err)
	}
	if err = this.conn.writeSync(flag_msg, body); err != nil {
		return fmt.Errorf("%w,write verify req err:%w", ErrClient, err)
	}

	_, body, err = this.conn.readSync()
	if err != nil {
		return fmt.Errorf("%w,read verify res err:%w", ErrClient, err)
	}
	var res msg.MsgVerifyRes
	if this.envCoder, err = decodeEnvelope(body, &res); err != nil {
		return fmt.Errorf("%w,decode verify res err:%w", ErrClient, err)
	}
	if res.Err != "" {
		return fmt.Errorf("%w,%v", ErrServer, res.Err)
	}
	return nil
}

// HandleMsg 实现 Handler，由这条连接的 readPump 同步调用。
// PING/PONG 已在传输层消化，不会走到这里。
func (this *clientConn) HandleMsg(body []byte) error {
	var got msg.Msg
	if _, err := decodeEnvelope(body, &got); err != nil {
		return fmt.Errorf("%w,read body err:%w", ErrClientDecode, err)
	}
	switch got.T {
	case msgtype.Req, msgtype.ReqRandOne:
		go this.client.func_call(&got)
	case msgtype.Res, msgtype.ResRandOne, msgtype.On:
		this.client.resolve(&got)
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

// disconnect 连接结束：结算挂着的 call，复位 topic 注册标记。
// 锁顺序 rwl -> l，与 On / regist_topic_lock / Stop 一致。
func (this *Client) disconnect(cc *clientConn) {
	this.rwl.Lock()
	defer this.rwl.Unlock()
	this.l.Lock()
	defer this.l.Unlock()

	if this.cc == cc {
		this.cc = nil
		this.connecting = false
	}
	this.stopLocked(fmt.Errorf("%w,connection lost", ErrClient))
}

// Stop 主动断开当前连接（keepAlive 会再连）。err 会被结算到所有未完成的 call 上。
func (this *Client) Stop(err error) {
	this.rwl.Lock()
	defer this.rwl.Unlock()
	this.l.Lock()
	defer this.l.Unlock()

	if this.cc != nil {
		// Close 不阻塞：cancel + 关 socket，把阻塞在 Serve 里的泵顶开
		if e := this.cc.conn.Close(); e != nil && err == nil {
			err = fmt.Errorf("%w,close conn err:%w", ErrClient, e)
		}
		this.cc = nil
		this.connecting = false
	}
	this.stopLocked(err)
}

// Close 彻底关掉这个 Client：断开当前连接，并让 keepAlive 不再重连。幂等。
// 与 Stop 的区别：Stop 只断当前连接（会自动重连），Close 是终态。
// 不调 Close 的话，Dial 出来的 keepAlive goroutine 和连接会一直存在。
func (this *Client) Close() error {
	this.rwl.Lock()
	defer this.rwl.Unlock()
	this.l.Lock()
	defer this.l.Unlock()

	if this.stopped {
		return nil
	}
	this.stopped = true

	var err error
	if this.cc != nil {
		if e := this.cc.conn.Close(); e != nil {
			err = fmt.Errorf("%w,close conn err:%w", ErrClient, e)
		}
		this.cc = nil
		this.connecting = false
	}
	this.stopLocked(fmt.Errorf("%w,client closed", ErrClient))
	return err
}

// stopLocked 必须持有 rwl 写锁与 l
func (this *Client) stopLocked(err error) {
	for _, call := range this.pending {
		call.Err = err
		logrus.Errorf("%+v,err:%v", call.Msg, call.Err)
		call.done()
	}
	this.pending = make(map[uint64]*Call)
	// seq 不能归零。归零后新 call 会复用旧序号，而服务端回包带的是客户端序号：
	// 旧连接上迟到的 Res(seq=N) 就可能被结算到新连接刚建起来的 call(seq=N) 上。
	// 正常情况下旧连接的 readPump 已经退出、不会再投递消息，但这是靠时序保证的，
	// 不值得用"序号复用"去赌。uint64 单调递增没有耗尽风险。

	// topics 由 rwl 保护，调用方已持写锁，原来的 FIXME（不敢加锁）就此解决
	for tp := range this.topics {
		tp.IsRegistSuccess = false
	}
}

func (this *Client) parse(coderType coder.T, req_body_data []byte, req_args_count int, m *method) (argsValue []reflect.Value, err error) {
	argsValue = make([]reflect.Value, m.argsCount)
	rest := req_body_data
	for i := 0; i < m.argsCount; i++ {
		argType := m.argsType[i]
		argValue := reflect.New(argType.at)
		if i < req_args_count {
			if rest, err = coder.Unmarshal(coderType, rest, argValue.Interface()); err != nil {
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
	this.l.Lock()
	err := this.writeLocked(m)
	cc := this.cc
	this.l.Unlock()

	if err != nil && cc != nil {
		// 写不动基本等于连接已废，主动关掉让 keepAlive 走重连
		if e := cc.conn.Close(); e != nil {
			logrus.Error(e)
		}
	}
	return err
}

// writeLocked 必须持有 l
func (this *Client) writeLocked(m *msg.Msg) error {
	if !this.connecting || this.cc == nil {
		return fmt.Errorf("%w %w connecting:%v", ErrClient, ErrNoConnect, this.connecting)
	}
	body, err := encodeEnvelope(this.cc.envCoder, m)
	if err != nil {
		return fmt.Errorf("%w,encode msg err:%w", ErrClient, err)
	}
	if err = this.cc.conn.write(flag_msg, body); err != nil {
		return fmt.Errorf("%w,conn write err:%w", ErrClient, err)
	}
	return nil
}

// regist_topic 把 On 注册的 topic 上报给服务端。
// 连接没起来或已断就直接返回——connect() 成功后会再拉一次，不必空转报错。
func (this *Client) regist_topic() {
	for {
		if !this.isConnected() {
			return
		}
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
		Coder:     this.coderType,
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

func (this *Client) emit(t msgtype.T, en eventname.T, args ...any) error {
	call := <-this.emit_async(t, en, args...).Done
	return call.Err
}

func (this *Client) send(call *Call) {
	seq := atomic.AddUint64(&this.seq, 1)
	call.Msg.Seq = seq

	this.l.Lock()
	this.pending[seq] = call
	err := this.writeLocked(call.Msg)
	if err != nil {
		delete(this.pending, seq)
		call.Err = err
		call.done()
	}
	this.l.Unlock()
}
