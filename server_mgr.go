package event

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/antlabs/timer"
	"github.com/google/uuid"
	"github.com/ndsky1003/event/v2/msg"
	"github.com/ndsky1003/event/v2/msgtype"
	"github.com/ndsky1003/event/v2/topic"
	"github.com/ndsky1003/net/v2/server"
	"github.com/vmihailenco/msgpack/v5"
)

// eventServer 实现 server_manager 接口
type eventServer struct {
	opt *ServerOption

	// 内部状态
	l        sync.Mutex
	seq      uint64
	tm       timer.Timer
	monitor  map[*topic.Topic]map[uuid.UUID]struct{}
	sessions map[uuid.UUID]server.Session // 保存 Session 引用
	pending  map[uint64]*server_call
	ctx      context.Context
	cancel   context.CancelFunc
}

// server_call 服务端调用状态
type server_call struct {
	tn         timer.TimeNoder // 超时检测
	originSid  uuid.UUID       // 原始的 sid
	originSeq  uint64          // frame.需要改成服务器的 seq

	serverReqCount uint64
	errs           []error
}

// newEventServer 创建事件服务器
func newEventServer(opts ...*ServerOption) *eventServer {
	ctx, cancel := context.WithCancel(context.Background())
	opt := ServerOptions().
		SetSecret("").
		SetTimeout(10 * time.Second).
		SetIsWrapError(true).
		merges(opts...)

	s := &eventServer{
		opt:      opt,
		tm:       timer.NewTimer(),
		monitor:  make(map[*topic.Topic]map[uuid.UUID]struct{}),
		sessions: make(map[uuid.UUID]server.Session),
		pending:  make(map[uint64]*server_call),
		ctx:      ctx,
		cancel:   cancel,
	}
	go s.tm.Run()
	return s
}

// OnConnect 连接建立时调用 - 验证逻辑
func (s *eventServer) OnConnect(sess server.Session) error {
	// 使用 conn.Write/Read/Flush 进行握手验证
	c := sess.Conn()

	// 1. 读取验证请求
	data, err := c.Read()
	if err != nil {
		return fmt.Errorf("read verify request failed: %w", err)
	}

	// 2. 解码验证请求
	verifyReq, err := decodeVerifyReq(data)
	if err != nil {
		return fmt.Errorf("decode verify request failed: %w", err)
	}

	// 3. 验证 secret
	if *s.opt.Secret != "" && verifyReq.Secret != *s.opt.Secret {
		verifyRes := &msg.MsgVerifyRes{Err: "invalid secret"}
		data, _ = encodeVerifyRes(verifyRes)
		c.Write(data)
		c.Flush()
		return errors.New("invalid secret")
	}

	// 4. 返回验证成功
	verifyRes := &msg.MsgVerifyRes{}
	data, err = encodeVerifyRes(verifyRes)
	if err != nil {
		return fmt.Errorf("encode verify response failed: %w", err)
	}
	if err := c.Write(data); err != nil {
		return fmt.Errorf("write verify response failed: %w", err)
	}
	if err := c.Flush(); err != nil {
		return fmt.Errorf("flush verify response failed: %w", err)
	}

	// 5. 保存 Session 引用
	s.l.Lock()
	s.sessions[sess.ID()] = sess
	s.l.Unlock()

	slog.Info("event service ready", "name", verifyReq.Name, "id", sess.ID())
	return nil
}

// OnMessage 收到消息时调用
func (s *eventServer) OnMessage(sess server.Session, data []byte) error {
	// 解码消息
	m, err := decodeMsg(data)
	if err != nil {
		slog.Error("decode message failed", "err", err)
		return err
	}

	// 根据消息类型分发
	switch m.T {
	case msgtype.On:
		s.on(sess.ID(), m)
	case msgtype.ReqAll, msgtype.ReqOne, msgtype.ReqFirst:
		s.req(sess.ID(), m)
	case msgtype.Res, msgtype.ResFirst:
		s.res(sess.ID(), m)
	default:
		slog.Info("discard message", "id", sess.ID(), "type", m.T)
	}
	return nil
}

// OnDisconnect 连接断开时调用
func (s *eventServer) OnDisconnect(sess server.Session, err error) error {
	s.l.Lock()
	defer s.l.Unlock()

	sid := sess.ID()

	// 从 monitor 删除
	for _, sids := range s.monitor {
		delete(sids, sid)
	}

	// 从 sessions 删除
	delete(s.sessions, sid)

	slog.Info("service disconnected", "id", sid, "err", err)
	return nil
}

// Close 关闭服务器
func (s *eventServer) Close() error {
	s.cancel()
	s.tm.Stop()
	s.l.Lock()
	defer s.l.Unlock()
	s.monitor = make(map[*topic.Topic]map[uuid.UUID]struct{})
	s.sessions = make(map[uuid.UUID]server.Session)
	s.pending = make(map[uint64]*server_call)
	return nil
}

// ============ 原有业务逻辑迁移 ============

// on 处理订阅注册
func (s *eventServer) on(sid uuid.UUID, frame *msg.Msg) {
	en := frame.EventName
	s.l.Lock()
	var isExist bool
	for tp, sids := range s.monitor {
		if tp.Equal(en) {
			sids[sid] = struct{}{}
			isExist = true
			break
		}
	}
	if !isExist {
		v := map[uuid.UUID]struct{}{sid: {}}
		s.monitor[topic.New(en)] = v
	}
	s.l.Unlock()

	// 写回确认（通过 session）
	// TODO: 需要能获取 Session 来发送
}

// req 处理请求
func (s *eventServer) req(sid uuid.UUID, frame *msg.Msg) {
	et := frame.EventName
	serverSeq := atomic.AddUint64(&s.seq, 1)
	originSeq := frame.Seq
	frame.Seq = serverSeq

	sCall := &server_call{
		originSid: sid,
		originSeq: originSeq,
	}

	// 收集所有匹配的 session IDs - 预分配容量
	var matchedSessions []uuid.UUID
	s.l.Lock()
	if len(s.monitor) > 0 {
		// 估算容量，减少扩容
		estimatedCap := len(s.monitor) * 2 // 假设每个 topic 平均 2 个监听者
		matchedSessions = make([]uuid.UUID, 0, estimatedCap)
		for tp, v := range s.monitor {
			if tp.Match(et) {
				for sid := range v {
					matchedSessions = append(matchedSessions, sid)
				}
			}
		}
	}
	s.l.Unlock()

	if len(matchedSessions) == 0 {
		// 没有监听者，直接返回空响应
		frame.T = msgtype.Res
		frame.Seq = originSeq
		frame.Bytes = nil
		frame.BodyCount = 0
		s.writeToSession(sid, frame)
		return
	}

	// 根据消息类型处理
	switch frame.T {
	case msgtype.ReqAll:
		s.reqAll(matchedSessions, sCall, serverSeq, frame, sid)
	case msgtype.ReqOne:
		s.reqOne(matchedSessions, sCall, serverSeq, frame, sid)
	case msgtype.ReqFirst:
		s.reqFirst(matchedSessions, sCall, serverSeq, frame, sid)
	}
}

// reqAll 发送给所有，等待所有响应
func (s *eventServer) reqAll(matchedSessions []uuid.UUID, sCall *server_call, serverSeq uint64, frame *msg.Msg, originSid uuid.UUID) {
	sCall.serverReqCount = uint64(len(matchedSessions))

	s.l.Lock()
	s.pending[serverSeq] = sCall
	s.l.Unlock()

	// 发送给所有匹配的 session
	for _, sid := range matchedSessions {
		s.writeToSession(sid, frame)
	}

	// 启动超时
	sCall.tn = s.tm.AfterFunc(*s.opt.Timeout*time.Second, func() {
		s.releaseTimeout(serverSeq)
	})
}

// reqOne 随机发送给一个监听者
func (s *eventServer) reqOne(matchedSessions []uuid.UUID, sCall *server_call, serverSeq uint64, frame *msg.Msg, originSid uuid.UUID) {
	// 随机选择一个
	randomIdx := rand.Intn(len(matchedSessions))
	selectedSid := matchedSessions[randomIdx]

	sCall.serverReqCount = 1

	s.l.Lock()
	s.pending[serverSeq] = sCall
	s.l.Unlock()

	// 发送给选中的 session
	s.writeToSession(selectedSid, frame)

	// 启动超时
	sCall.tn = s.tm.AfterFunc(*s.opt.Timeout*time.Second, func() {
		s.releaseTimeout(serverSeq)
	})
}

// reqFirst 发送给所有，收到第一个响应就返回
func (s *eventServer) reqFirst(matchedSessions []uuid.UUID, sCall *server_call, serverSeq uint64, frame *msg.Msg, originSid uuid.UUID) {
	sCall.serverReqCount = uint64(len(matchedSessions))

	s.l.Lock()
	s.pending[serverSeq] = sCall
	s.l.Unlock()

	// 发送给所有匹配的 session
	for _, sid := range matchedSessions {
		s.writeToSession(sid, frame)
	}

	// 启动超时
	sCall.tn = s.tm.AfterFunc(*s.opt.Timeout*time.Second, func() {
		s.releaseTimeout(serverSeq)
	})
}

// res 处理响应
func (s *eventServer) res(sid uuid.UUID, m *msg.Msg) {
	serverSeq := m.Seq
	s.l.Lock()
	sCall, ok := s.pending[serverSeq]
	if ok {
		sCall.serverReqCount--
		leftCount := sCall.serverReqCount
		if e := m.Err; e != "" {
			var ee error
			if *s.opt.IsWrapError {
				// 获取 session name 来包装错误
				s.l.Unlock()
				sess, sessionOk := s.sessions[sid]
				if sessionOk && sess != nil {
					ee = fmt.Errorf("Name:[%v],Err:[%s]", sess.ID(), e)
				} else {
					ee = fmt.Errorf("Name:[%s],Err:[%s]", sid, e)
				}
				s.l.Lock()
			} else {
				ee = errors.New(e)
			}
			sCall.errs = append(sCall.errs, ee)
		}

		// ResFirst: 收到第一个响应就返回
		// ResAll: 等待所有响应
		shouldReturn := (leftCount == 0 && m.T == msgtype.Res) || m.T == msgtype.ResFirst

		if shouldReturn {
			if len(sCall.errs) > 0 {
				m.Err = errors.Join(sCall.errs...).Error()
			}
			m.Seq = sCall.originSeq
			if sCall.tn != nil {
				sCall.tn.Stop()
			}
			delete(s.pending, serverSeq)
		}
	}
	s.l.Unlock()

	if ok {
		shouldSend := (sCall.serverReqCount == 0 && m.T == msgtype.Res) || m.T == msgtype.ResFirst
		if shouldSend {
			s.writeToSession(sCall.originSid, m)
		}
	}
}

// writeToSession 向指定 session 发送消息
func (s *eventServer) writeToSession(sid uuid.UUID, m *msg.Msg) {
	s.l.Lock()
	sess, ok := s.sessions[sid]
	s.l.Unlock()
	if !ok {
		return
	}

	// 编码消息
	data, err := encodeMsg(m)
	if err != nil {
		slog.Error("encode message failed", "err", err)
		return
	}

	// 发送消息
	if err := sess.Send(s.ctx, data); err != nil {
		slog.Error("send message to session failed", "id", sid, "err", err)
	}
}

// releaseTimeout 处理超时
func (s *eventServer) releaseTimeout(serverSeq uint64) {
	s.l.Lock()
	sCall, ok := s.pending[serverSeq]
	if ok {
		if sCall.tn != nil {
			sCall.tn.Stop()
		}
		delete(s.pending, serverSeq)
	}
	s.l.Unlock()

	if ok {
		frame := &msg.Msg{
			T:   msgtype.Res,
			Seq: sCall.originSeq,
			Err: fmt.Errorf("%w,timeout", ErrServer).Error(),
		}
		s.writeToSession(sCall.originSid, frame)
	}
}

// 辅助函数：编码/解码验证消息
func decodeVerifyReq(data []byte) (msg.MsgVerifyReq, error) {
	var req msg.MsgVerifyReq
	dec := msgpack.GetDecoder()
	defer msgpack.PutDecoder(dec)

	reader := readerPool.Get().(*bytes.Reader)
	reader.Reset(data)
	dec.Reset(reader)
	defer readerPool.Put(reader)

	err := dec.Decode(&req)
	return req, err
}

func encodeVerifyRes(res *msg.MsgVerifyRes) ([]byte, error) {
	buf := bufPool.Get().(*bytes.Buffer)
	defer bufPool.Put(buf)
	buf.Reset()
	enc := msgpack.GetEncoder()
	defer msgpack.PutEncoder(enc)
	enc.Reset(buf)
	err := enc.Encode(res)
	if err != nil {
		return nil, err
	}
	result := make([]byte, buf.Len())
	copy(result, buf.Bytes())
	return result, nil
}
