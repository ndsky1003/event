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
)

// 锁顺序固定为 rwl -> l：持 l 的代码路径不得再去拿 rwl，
// 否则会和 On / regist_topic_lock 交叉死锁。
type Client struct {
	url    string
	seq    atomic.Uint64
	opt    *ClientOption
	closed atomic.Bool
	ctx    context.Context
	cancel context.CancelFunc

	rwl    sync.RWMutex // protect topics 与 conn；On / SetConn / disconnect / Close 写
	topics []topicEntry

	l       sync.Mutex // protect conn / pending
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

// maxBackoffFactor 重连退避的上限倍数：拨号/握手连续失败时，间隔从
// check_interval 指数翻倍，封顶到 check_interval * maxBackoffFactor（默认 2s → 32s）。
const maxBackoffFactor = 16

// keepAlive 拨号 → 握手 → 跑连接 → 断了再连。
// 心跳与断连检测已经下沉到 Conn 的看门狗，这里不再发应用层 Ping。
// 拨号/握手失败时指数退避（避免服务器不可达时疯狂重试刷日志），
// 连接成功建立过则退避归零。Close 之后退出，不再重连。
func (this *Client) keepAlive() {
	interval := *this.opt.check_interval
	backoff := interval
	maxBackoff := interval * maxBackoffFactor
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

		// 拨号/握手失败则指数退避，成功建立过则归零
		if conn == nil {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		} else {
			backoff = interval
		}

		// 重连间隔（响应 Close，不再用 time.Sleep）
		select {
		case <-time.After(backoff):
		case <-this.ctx.Done():
			return
		}
	}
}

// connect 建立连接、完成握手，然后阻塞到连接结束，返回连接与终止原因。
// 握手失败会关闭连接并置 conn 为 nil，避免泄漏尚未 Serve 的连接。
func (this *Client) connect(ctx context.Context) (conn *Conn, err error) {
	raw, err := net.Dial("tcp", this.url)
	if err != nil {
		return nil, fmt.Errorf("%w dial err: %w", ErrClient, err)
	}
	conn = New(ctx, raw, this, &this.opt.connOption)
	if err = this.handshake(conn); err != nil {
		conn.CloseWithError(err)
		return nil, err
	}
	this.SetConn(conn)
	go this.regist_topic()
	err = conn.Serve()
	return conn, err
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

// HandleMsg 实现 Handler，由这条连接的 msgWorker 串行调用。
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
		slog.Info("invalid msg", "msg", got)
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

// SetConn 换上新的连接，关闭旧连接并结算它欠的 pending。
// 只在 connect 成功握手后调用；重连成功后旧连接上未完成的响应一并作废。
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

// disconnect 在连接结束时收尾：清掉 conn 指针并结算它欠的 pending。
// 仅当当前连接就是 conn 时才处理，避免迟到的旧连接事件污染新连接。
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

// Close 关闭客户端并终止重连循环，幂等。第一次调用返回关闭原因（ErrClient 包装），
// 之后返回 nil。返回值仅是原因说明，关闭本身不会失败，可安全忽略。
func (this *Client) Close() error {
	return this.CloseWithErr(fmt.Errorf("%w shutdown", ErrClient))
}

// CloseWithErr 关闭客户端并指定关闭原因。幂等：只有第一个调用者真正执行关闭并
// 返回该原因，后续调用返回 nil。err 为 nil 时回退为 ErrClient。
func (this *Client) CloseWithErr(err error) error {
	if !this.closed.CompareAndSwap(false, true) {
		return nil
	}
	if err == nil {
		err = ErrClient
	}
	this.rwl.Lock()
	defer this.rwl.Unlock()
	this.l.Lock()
	defer this.l.Unlock()

	this.cancel()
	if this.conn != nil {
		this.conn = nil
		this.stopLocked(fmt.Errorf("%w,%w client closed", ErrClient, err))
	}
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
	// 回调 panic 时兜底回一个错误包：否则服务端 pending 干等 Timeout（默认 10s）。
	// 这里的 recover 只保护当前 goroutine（func_call 是被 go 出来的），
	// dispatchMsg 的 recover 覆盖不到它。
	defer func() {
		if r := recover(); r != nil {
			slog.Error("func_call panic", "event", et, "err", r)
			res.Err = fmt.Errorf("%w,回调 panic:%v", ErrClient, r).Error()
			if err := this.Write(res); err != nil {
				slog.Error("func_call write res", "err", err)
			}
		}
	}()
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
		slog.Error("func_call write res", "err", err)
	}
}

// Write 编码并发送一条消息。消息框架固定用 coder.Msgp（与服务端一致），
// 事件参数才走 Msg.Coder 声明的编码。失败只返回错误，连接收尾交给 Serve 的收尾流程。
func (this *Client) Write(m *msg.Msg) error {
	var c *Conn
	this.l.Lock()
	c = this.conn
	this.l.Unlock()
	if c == nil {
		return fmt.Errorf("%w client connection is nil", ErrClient)
	}
	body, err := coder.Encode(coder.Msgp, nil, m)
	if err != nil {
		return fmt.Errorf("%w, err:%w", ErrClient, err)
	}
	return c.Write(body)

}

// regist_topic 把 On 注册的 topic 上报给服务端，失败则隔 2s 重试直到全部成功。
// 它同步阻塞 On，保证 On 返回时 topic 已在服务端就位，紧随其后的 Emit 不会落空。
// Close 之后退出：循环和重试等待都检查 ctx.Done，避免服务器不可达时 On 永久阻塞。
func (this *Client) regist_topic() {
	for {
		select {
		case <-this.ctx.Done():
			return
		default:
		}
		if err := this.regist_topic_lock(); err != nil {
			select {
			case <-time.After(2 * time.Second):
			case <-this.ctx.Done():
				return
			}
			continue
		}
		return
	}
}

// regist_topic_lock 锁顺序 rwl -> l（emit → send 会拿 l）
func (this *Client) regist_topic_lock() error {
	opt := SendOptions()
	opt.mergeClientOption(this.opt)
	var errs []error
	this.rwl.Lock()
	for i := range this.topics {
		tp := this.topics[i].tp
		if !tp.IsRegistSuccess {
			if call := <-this.emit_async(msgtype.On, tp.GetEventName(), *opt).Done; call.Err != nil {
				err := fmt.Errorf("%w, emit_on:[%v] err:%w", ErrClient, tp.GetEventName(), call.Err)
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
		call.Err = fmt.Errorf("%w,%v", ErrClient, "event name empty")
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
	m.Bytes = nil
	if len(args) > 0 {
		var err error
		for _, arg := range args {
			if m.Bytes, err = coder.Encode(m.Coder, m.Bytes, arg); err != nil {
				err = fmt.Errorf("%w: %w", ErrClientEncode, err)
				break
			}
		}
		if err != nil {
			m.Bytes = nil
			call.Err = err
			call.done()
			return
		}
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
