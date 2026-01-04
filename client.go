package event

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ndsky1003/buffer"
	"github.com/ndsky1003/event/v3/eventname"
	"github.com/ndsky1003/event/v3/msg"
	"github.com/ndsky1003/event/v3/msgtype"
	"github.com/ndsky1003/event/v3/topic"
	"github.com/ndsky1003/net/v2/client"
	"github.com/ndsky1003/net/v2/conn"
	"github.com/vmihailenco/msgpack/v5"
)

// Client 事件客户端
type Client struct {
	url   string
	opt   *ClientOption
	netCl *client.Client
	seq   uint64

	rwl    sync.RWMutex
	topics map[*topic.Topic][]*method

	l            sync.Mutex
	pending      map[uint64]*Call
	registing    bool          // 是否正在注册 topics
	registDoneCh chan struct{} // 注册完成通知
}

// Dial 连接到服务器
func Dial(url string, opts ...*ClientOption) *Client {
	opt := ClientOptions().
		SetName("").
		SetSecret("").
		SetIsWrapError(true).
		Merge(opts...)

	c := &Client{
		url:          url,
		topics:       make(map[*topic.Topic][]*method),
		pending:      make(map[uint64]*Call),
		opt:          opt,
		registDoneCh: make(chan struct{}),
	}

	// 创建 handler
	handler := &eventHandler{client: c}

	// 使用 net.Client.Dial
	ctx, cancel := context.WithCancel(context.Background())
	netCl, err := client.Dial(ctx, *opt.Name, url,
		client.Options().
			SetHandler(handler).
			SetOnConnected(c.onConnect).
			WithConn(func(copt *conn.Option) {
				// 设置 buffer 生成函数
				copt.SetGenBufFn(func() []byte {
					return make([]byte, 1024*4)
				})
			}),
	)
	if err != nil {
		slog.Error("dial failed", "err", err)
		cancel()
		return c
	}

	c.netCl = netCl
	go func() {
		<-ctx.Done()
		c.Stop(context.Canceled)
	}()

	return c
}

// onConnect 连接成功后的验证逻辑
func (c *Client) onConnect(cnn *conn.Conn) error {
	// 1. 发送验证请求
	verifyReq := &msg.MsgVerifyReq{
		Name:   *c.opt.Name,
		Secret: *c.opt.Secret,
	}
	buf := bufPool.Get().(*bytes.Buffer)
	defer bufPool.Put(buf)
	buf.Reset()
	enc := msgpack.GetEncoder()
	defer msgpack.PutEncoder(enc)
	enc.Reset(buf)
	if err := enc.Encode(verifyReq); err != nil {
		return fmt.Errorf("encode verify request failed: %w", err)
	}

	// 使用 conn.Write 发送
	if err := cnn.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("write verify request failed: %w", err)
	}
	if err := cnn.Flush(); err != nil {
		return fmt.Errorf("flush verify request failed: %w", err)
	}

	// 2. 读取验证响应
	data, err := cnn.Read()
	if err != nil {
		return fmt.Errorf("read verify response failed: %w", err)
	}

	var verifyRes msg.MsgVerifyRes
	dec := msgpack.GetDecoder()
	defer msgpack.PutDecoder(dec)

	reader := readerPool.Get().(*bytes.Reader)
	reader.Reset(data)
	dec.Reset(reader)
	defer readerPool.Put(reader)

	if err := dec.Decode(&verifyRes); err != nil {
		return fmt.Errorf("decode verify response failed: %w", err)
	}

	if verifyRes.Err != "" {
		return errors.New(verifyRes.Err)
	}

	// 3. 注册 topics
	go c.registTopic()

	return nil
}

// Stop 停止客户端
func (c *Client) Stop(err error) {
	c.l.Lock()
	// 取消之前的注册
	select {
	case <-c.registDoneCh:
	default:
		close(c.registDoneCh)
	}

	for seq, call := range c.pending {
		call.Err = err
		slog.Error("call error", "msg", call.Msg, "err", call.Err)
		call.done()
		delete(c.pending, seq)
	}

	// 重置 topics 注册状态
	c.rwl.Lock()
	for tp := range c.topics {
		tp.IsRegistSuccess = false
	}
	c.rwl.Unlock()

	// 不关闭 netCl，让 net/v2 自动重连
	c.seq = 0
	c.pending = make(map[uint64]*Call)

	// 重新创建 registDoneCh
	c.registDoneCh = make(chan struct{})
	c.l.Unlock()
}

// registTopic 注册所有 topics
func (c *Client) registTopic() {
	c.l.Lock()
	if c.registing {
		c.l.Unlock()
		return
	}
	c.registing = true
	c.l.Unlock()

	defer func() {
		c.l.Lock()
		c.registing = false
		c.l.Unlock()
	}()

	for {
		select {
		case <-c.registDoneCh:
			return
		default:
		}
		if err := c.registTopicLock(); err != nil {
			select {
			case <-c.registDoneCh:
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}
		return
	}
}

func (c *Client) registTopicLock() error {
	// 先收集需要注册的 topics，避免持有锁时进行网络调用
	type pendingTopic struct {
		topic *topic.Topic
		name  eventname.T
	}
	var pending []pendingTopic

	c.rwl.Lock()
	for tp := range c.topics {
		if !tp.IsRegistSuccess {
			pending = append(pending, pendingTopic{topic: tp, name: tp.GetEventName()})
		}
	}
	c.rwl.Unlock()

	// 释放锁后再进行注册
	var errs []error
	for _, p := range pending {
		if err := c.emit(msgtype.On, p.name); err != nil {
			err := fmt.Errorf("%w, emit_on:[%v] err:%w", ErrClient, p.name, err)
			slog.Error("emit_on failed", "topic", p.name, "err", err)
			errs = append(errs, err)
		} else {
			p.topic.IsRegistSuccess = true
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// Write 写入消息
func (c *Client) Write(m *msg.Msg) error {
	c.l.Lock()
	defer c.l.Unlock()
	if err := c.write(m); err != nil {
		// write 失败，net/v2 会自动重连，这里只需要重置注册状态
		c.rwl.Lock()
		for tp := range c.topics {
			tp.IsRegistSuccess = false
		}
		c.rwl.Unlock()
		return err
	}
	return nil
}

func (c *Client) write(m *msg.Msg) error {
	if c.netCl == nil || !c.netCl.IsConnected() {
		return ErrNoConnect
	}

	// 编码消息
	data, err := encodeMsg(m)
	if err != nil {
		return fmt.Errorf("encode message failed: %w", err)
	}

	// 发送
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.netCl.Send(ctx, data)
}

// emitAsync 异步发送
func (c *Client) emitAsync(t msgtype.T, en eventname.T, args ...any) *Call {
	m := &msg.Msg{
		T:         t,
		EventName: en,
		BodyCount: int8(len(args)),
	}
	call := NewCall(m)
	if m.EventName == "" {
		call.Err = fmt.Errorf("%w,%v", ErrServer, "event name empty")
		call.done()
	}

	if len(args) > 0 {
		buf := buffer.Get()
		defer buf.Release()
		paramEncoder := msgpack.GetEncoder()
		defer msgpack.PutEncoder(paramEncoder)
		paramEncoder.Reset(buf)
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
			return call
		}
		m.Bytes = buf.Bytes()
	}

	c.send(call)
	return call
}

// emit 同步发送
func (c *Client) emit(t msgtype.T, en eventname.T, args ...any) error {
	call := <-c.emitAsync(t, en, args...).Done
	return call.Err
}

// send 发送调用
func (c *Client) send(call *Call) {
	seq := atomic.AddUint64(&c.seq, 1)
	var err error

	c.l.Lock()
	defer c.l.Unlock()
	c.pending[seq] = call
	call.Msg.Seq = seq

	if c.netCl == nil || !c.netCl.IsConnected() {
		err = fmt.Errorf("%w %w connecting:%v", ErrClient, ErrNoConnect, c.netCl != nil && c.netCl.IsConnected())
	}

	if err == nil {
		err = c.write(call.Msg)
	}

	if err != nil {
		delete(c.pending, seq)
		call.Err = err
		call.done()
	}
}
