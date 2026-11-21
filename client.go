package event

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ndsky1003/event/v3/coder"
	"github.com/ndsky1003/event/v3/eventname"
	"github.com/ndsky1003/event/v3/msg"
	"github.com/ndsky1003/event/v3/msgtype"
)

// nodeCall 一次 Emit 的跨 peer 聚合状态。
// 一个 Emit 可能要投递给多个 peer，等它们全部回包（或最快的那个）才算完成。
type nodeCall struct {
	call      *Call
	peerCount uint64 // 还有几个 peer 没回包
	targets   []string
	settled   map[string]struct{}
	errs      []error
	localErr  error
	randOne   bool
}

// Client p2p 节点：既是发布者也是订阅者。
// 它连服务端节点拿 peer 列表，主动连接老节点、接受新节点连接，
// 通过订阅广播维护订阅表，事件按订阅表点对点路由。
// 锁顺序固定为 rwl -> l：持 l 的代码路径不得再去拿 rwl。
type Client struct {
	name        string
	listenAddrs []string
	serverAddr  string
	opt         *ClientOption
	seq         atomic.Uint64
	closed      atomic.Bool
	ctx         context.Context
	cancel      context.CancelFunc

	rwl    sync.RWMutex // protect topics
	topics []topicEntry // 本地订阅

	l           sync.Mutex // protect serverConn / peers / wantPeers / subs / pending / lns / actualAddrs
	serverConn  *Conn
	peers       map[string]*Conn
	wantPeers   map[string][]string // 应该连接的 peer：name -> addrs
	subs        map[eventname.T]map[string]struct{}
	pending     map[uint64]*nodeCall
	lns         []net.Listener
	actualAddrs []string // 监听成功后回填的实际地址列表（:0 时是系统分配的随机端口）
}

// maxBackoffFactor 重连退避的上限倍数：拨号/握手连续失败时，间隔从
// check_interval 指数翻倍，封顶到 check_interval * maxBackoffFactor。
const maxBackoffFactor = 16

func Dial(serverAddr string, opts ...*ClientOption) *Client {
	c := &Client{
		serverAddr: serverAddr,
		peers:      map[string]*Conn{},
		wantPeers:  map[string][]string{},
		subs:       map[eventname.T]map[string]struct{}{},
		pending:    map[uint64]*nodeCall{},
	}
	c.opt = ClientOptions().
		SetName("").
		SetListenAddr("127.0.0.1:0").
		SetCheckInterval(2 * time.Second).
		SetHeartInterval(10 * time.Second).
		SetSecret("").
		SetIsWrapError(true).
		SetCoderType(coder.Msgp).
		merges(opts...)
	c.name = *c.opt.name
	c.listenAddrs = c.opt.listen_addrs
	c.ctx, c.cancel = context.WithCancel(context.TODO())

	c.startListen()
	go c.keepAliveServer()
	return c
}

// ---------------------------------------------------------------------------
// 监听（接受其他节点的连接）
// ---------------------------------------------------------------------------

// startListen 同步监听所有地址（net.Listen 很快），成功后回填实际地址，再起 accept 循环。
// 同步是为了在连服务端注册之前就把实际监听地址定下来：SetListenAddr(":0") 时，
// 注册到服务端的是系统分配的真实端口，别的节点才能连上。
func (this *Client) startListen() {
	var lns []net.Listener
	var actuals []string
	for _, addr := range this.listenAddrs {
		ln, err := listenAddr(addr)
		if err != nil {
			slog.Error("node listen", "addr", addr, "err", err)
			continue
		}
		actual := addr
		if !strings.HasPrefix(addr, "unix://") {
			// TCP：回填实际监听地址（SetListenAddr(":0") 时是系统分配的随机端口）。
			// UDS 没有随机端口概念，直接用原始地址（保留 unix:// 前缀，别的节点才能识别）。
			actual = ln.Addr().String()
		}
		lns = append(lns, ln)
		actuals = append(actuals, actual)
	}
	if len(lns) == 0 {
		return
	}
	this.l.Lock()
	if this.closed.Load() {
		this.l.Unlock()
		for _, ln := range lns {
			_ = ln.Close()
		}
		return
	}
	this.lns = lns
	this.actualAddrs = actuals
	this.l.Unlock()
	for i, ln := range lns {
		slog.Info("event node listening", "addr", actuals[i])
		go this.acceptLoop(ln)
	}
}

func (this *Client) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if this.closed.Load() {
				return
			}
			slog.Error("accept", "err", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go this.handlePeerIn(conn)
	}
}

// handlePeerIn 入向 peer 连接：握手拿对方 name，登记，跑连接。
// 不主动重连——对方（主动连接方）会负责重连。
func (this *Client) handlePeerIn(raw net.Conn) {
	conn := New(this.ctx, raw, nil, &this.opt.connOption)
	name, err := serverSideVerify(conn, *this.opt.secret)
	if err != nil {
		slog.Error("peer handshake", "err", err)
		_ = conn.Close()
		return
	}
	conn.SetHandler(&peerHandler{c: this, name: name, conn: conn})

	this.l.Lock()
	this.peers[name] = conn
	this.l.Unlock()
	go this.broadcastAllSubscribe(conn)

	if err := conn.Serve(); err != nil && !errors.Is(err, ErrConnClosed) {
		slog.Error("peer die", "name", name, "err", err)
	}
	this.disconnectPeer(name, conn)
}

// ---------------------------------------------------------------------------
// 连服务端
// ---------------------------------------------------------------------------

func (this *Client) keepAliveServer() {
	interval := *this.opt.check_interval
	backoff := interval
	maxBackoff := interval * maxBackoffFactor
	for {
		select {
		case <-this.ctx.Done():
			return
		default:
		}
		conn, err := this.connectServer(this.ctx)
		if conn != nil {
			this.disconnectServer(conn)
		}
		if err != nil {
			slog.Error("keepAliveServer", "err", err)
		}
		if conn == nil {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		} else {
			backoff = interval
		}
		select {
		case <-time.After(backoff):
		case <-this.ctx.Done():
			return
		}
	}
}

func (this *Client) connectServer(ctx context.Context) (conn *Conn, err error) {
	raw, err := dialAddr(this.serverAddr)
	if err != nil {
		return nil, fmt.Errorf("%w dial dir err: %w", ErrClient, err)
	}
	conn = New(ctx, raw, nil, &this.opt.connOption)
	if err = clientSideVerify(conn, this.name, *this.opt.secret); err != nil {
		conn.CloseWithError(err)
		return nil, err
	}
	// 发 Register（name + 监听地址列表）。用实际监听地址（:0 时是系统分配的端口）
	this.l.Lock()
	regAddrs := this.actualAddrs
	this.l.Unlock()
	body, err := coder.Encode(coder.Msgp, nil, regAddrs)
	if err != nil {
		conn.CloseWithError(err)
		return nil, err
	}
	reg := &msg.Msg{T: msgtype.Register, Name: this.name, Bytes: body}
	body, err = coder.Encode(coder.Msgp, nil, reg)
	if err != nil {
		conn.CloseWithError(err)
		return nil, err
	}
	if err = conn.WriteToConn(5*time.Second, body); err != nil {
		conn.CloseWithError(err)
		return nil, err
	}
	conn.SetHandler(&serverClientHandler{c: this, conn: conn})
	this.l.Lock()
	this.serverConn = conn
	this.l.Unlock()
	err = conn.Serve()
	return conn, err
}

func (this *Client) disconnectServer(conn *Conn) {
	this.l.Lock()
	if this.serverConn == conn {
		this.serverConn = nil
	}
	this.l.Unlock()
}

// ---------------------------------------------------------------------------
// 连 peer（新节点主动连老节点）
// ---------------------------------------------------------------------------

func (this *Client) keepAlivePeer(name string, addrs []string) {
	interval := *this.opt.check_interval
	backoff := interval
	maxBackoff := interval * maxBackoffFactor
	for {
		select {
		case <-this.ctx.Done():
			return
		default:
		}
		this.l.Lock()
		_, want := this.wantPeers[name]
		this.l.Unlock()
		if !want {
			return
		}
		conn, err := this.connectPeer(this.ctx, name, addrs)
		if conn != nil {
			this.disconnectPeer(name, conn)
		}
		if err != nil {
			slog.Error("keepAlivePeer", "name", name, "err", err)
		}
		if conn == nil {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		} else {
			backoff = interval
		}
		select {
		case <-time.After(backoff):
		case <-this.ctx.Done():
			return
		}
	}
}

// connectPeer 按顺序尝试 addrs 里的每个地址，第一个握手成功的就建立连接。
func (this *Client) connectPeer(ctx context.Context, name string, addrs []string) (conn *Conn, err error) {
	var lastErr error
	for _, addr := range addrs {
		raw, err := dialAddr(addr)
		if err != nil {
			lastErr = err
			continue
		}
		c := New(ctx, raw, nil, &this.opt.connOption)
		if err = clientSideVerify(c, this.name, *this.opt.secret); err != nil {
			c.CloseWithError(err)
			lastErr = err
			continue
		}
		c.SetHandler(&peerHandler{c: this, name: name, conn: c})
		this.l.Lock()
		this.peers[name] = c
		this.l.Unlock()
		go this.broadcastAllSubscribe(c)

		err = c.Serve()
		return c, err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no addrs for peer %s", name)
	}
	return nil, fmt.Errorf("%w dial peer %s err: %w", ErrClient, name, lastErr)
}

// disconnectPeer peer 连接收尾：清 peers、清订阅表、结算该 peer 欠着的 pending。
func (this *Client) disconnectPeer(name string, conn *Conn) {
	this.l.Lock()
	if c := this.peers[name]; c == conn {
		delete(this.peers, name)
		for topic := range this.subs {
			delete(this.subs[topic], name)
		}
		// 结算该 peer 欠着的请求
		e := fmt.Errorf("%w,peer %s connection lost before response", ErrClient, name)
		for seq, nc := range this.pending {
			if _, dup := nc.settled[name]; dup {
				continue
			}
			if !nc.hasTarget(name) {
				continue
			}
			this.settleLocked(name, seq, e)
		}
	}
	this.l.Unlock()
}

func (this *nodeCall) hasTarget(name string) bool {
	for _, x := range this.targets {
		if x == name {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 服务端消息处理（RegisterRes / NodeDown）
// ---------------------------------------------------------------------------

func (this *Client) onRegisterRes(list []nodeInfo) {
	for _, ni := range list {
		if ni.Name == this.name {
			continue
		}
		this.l.Lock()
		_, exists := this.wantPeers[ni.Name]
		if !exists {
			this.wantPeers[ni.Name] = ni.Addrs
		}
		this.l.Unlock()
		if !exists {
			go this.keepAlivePeer(ni.Name, ni.Addrs)
		}
	}
}

func (this *Client) onNodeDown(name string) {
	this.l.Lock()
	delete(this.wantPeers, name)
	c := this.peers[name]
	this.l.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// ---------------------------------------------------------------------------
// 订阅广播
// ---------------------------------------------------------------------------

func (this *Client) onSubscribe(from string, en eventname.T) {
	this.l.Lock()
	m := this.subs[en]
	if m == nil {
		m = map[string]struct{}{}
		this.subs[en] = m
	}
	m[from] = struct{}{}
	this.l.Unlock()
}

func (this *Client) onUnsubscribe(from string, en eventname.T) {
	this.l.Lock()
	if m := this.subs[en]; m != nil {
		delete(m, from)
	}
	this.l.Unlock()
}

// SubscriberCount 返回当前订阅了 en 的远程 peer 数量（不含本地订阅者）。
// 订阅同步是异步的，发布前可用它确认订阅已到位（压测 warmup、观测都用它）。
func (this *Client) SubscriberCount(en eventname.T) int {
	this.l.Lock()
	defer this.l.Unlock()
	return len(this.subs[en])
}

// PeerCount 返回当前已建立连接的 peer 数量。
func (this *Client) PeerCount() int {
	this.l.Lock()
	defer this.l.Unlock()
	return len(this.peers)
}

// broadcastSubscribe 把"我订阅了 tp"广播给所有已连接的 peer。
func (this *Client) broadcastSubscribe(tp eventname.T) {
	m := &msg.Msg{T: msgtype.Subscribe, EventName: tp}
	this.l.Lock()
	conns := make([]*Conn, 0, len(this.peers))
	for _, c := range this.peers {
		conns = append(conns, c)
	}
	this.l.Unlock()
	for _, c := range conns {
		_ = this.writeConn(c, m)
	}
}

// broadcastAllSubscribe 把我所有的订阅广播给单个 peer（新连接建立时用）。
func (this *Client) broadcastAllSubscribe(conn *Conn) {
	this.rwl.RLock()
	tps := make([]eventname.T, 0, len(this.topics))
	for _, e := range this.topics {
		tps = append(tps, e.tp.GetEventName())
	}
	this.rwl.RUnlock()
	for _, tp := range tps {
		_ = this.writeConn(conn, &msg.Msg{T: msgtype.Subscribe, EventName: tp})
	}
}

// ---------------------------------------------------------------------------
// 事件路由与聚合
// ---------------------------------------------------------------------------

func (this *Client) resolve(from string, got *msg.Msg) {
	seq := got.Seq
	this.l.Lock()
	nc := this.pending[seq]
	if nc == nil {
		this.l.Unlock()
		return
	}
	if nc.randOne {
		delete(this.pending, seq)
		this.l.Unlock()
		if got.Err != "" {
			nc.call.Err = this.wrapRemoteErr(got.Err)
		}
		nc.call.done()
		return
	}
	if _, dup := nc.settled[from]; dup {
		this.l.Unlock()
		return
	}
	nc.settled[from] = struct{}{}
	nc.peerCount--
	if got.Err != "" {
		nc.errs = append(nc.errs, this.wrapRemoteErr(got.Err))
	}
	if nc.peerCount != 0 {
		this.l.Unlock()
		return
	}
	delete(this.pending, seq)
	this.l.Unlock()
	nc.call.Err = joinErrs(append([]error{nc.localErr}, nc.errs...)...)
	nc.call.done()
}

// settleLocked 结算"某个 peer 永远不会回包"。必须持 this.l。
func (this *Client) settleLocked(name string, seq uint64, err error) {
	nc := this.pending[seq]
	if nc == nil {
		return
	}
	if _, dup := nc.settled[name]; dup {
		return
	}
	nc.settled[name] = struct{}{}
	nc.peerCount--
	nc.errs = append(nc.errs, err)
	// randOne（EmitSomeOne）等最快的响应，单个 peer 断线不算结束；
	// 只有所有 peer 都断线（peerCount==0）才结算，否则 pending 永远挂着。
	if nc.peerCount != 0 {
		return
	}
	delete(this.pending, seq)
	nc.call.Err = joinErrs(append([]error{nc.localErr}, nc.errs...)...)
	nc.call.done()
}

func (this *Client) wrapRemoteErr(s string) error {
	if *this.opt.is_wrap_error {
		return fmt.Errorf("%w,%v", ErrServer, s)
	}
	return errors.New(s)
}

// ---------------------------------------------------------------------------
// 本地订阅执行与回调
// ---------------------------------------------------------------------------

// execLocal 在本地匹配并执行回调，返回是否有匹配的订阅者以及聚合后的错误。
func (this *Client) execLocal(req *msg.Msg) (has bool, err error) {
	et := req.EventName
	this.rwl.RLock()
	var matched []topicEntry
	for _, e := range this.topics {
		if e.tp.Match(et) {
			matched = append(matched, e)
		}
	}
	this.rwl.RUnlock()
	if len(matched) == 0 {
		return false, nil
	}
	var errs []error
	for _, e := range matched {
		tp := e.tp
		for _, m := range e.methods {
			args, err := this.parse(req.Coder, req.Bytes, int(req.BodyCount), m)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			returnValues := m.function.Call(args)
			errValue := returnValues[0]
			if errValue.IsValid() && !errValue.IsNil() {
				appendErr := errValue.Interface().(error)
				if *this.opt.is_wrap_error {
					appendErr = fmt.Errorf("[client:%v,topic:%v,event:%s,err:%w]", this.name, tp.GetEventName(), et, appendErr)
				}
				errs = append(errs, appendErr)
			}
		}
	}
	return true, joinErrs(errs...)
}

// func_call 收到远程 Req：本地执行回调并回 Res 给来源 peer。
func (this *Client) func_call(fromPeer string, req *msg.Msg) {
	t := msgtype.Res
	if req.T == msgtype.ReqRandOne {
		t = msgtype.ResRandOne
	}
	res := &msg.Msg{
		T:         t,
		Seq:       req.Seq,
		EventName: req.EventName,
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Error("func_call panic", "event", req.EventName, "err", r)
			res.Err = fmt.Errorf("%w,回调 panic:%v", ErrClient, r).Error()
			if err := this.writeToPeer(fromPeer, res); err != nil {
				slog.Error("func_call write res", "err", err)
			}
		}
	}()
	_, err := this.execLocal(req)
	if err != nil {
		res.Err = err.Error()
	}
	if err := this.writeToPeer(fromPeer, res); err != nil {
		slog.Error("func_call write res", "err", err)
	}
}

// ---------------------------------------------------------------------------
// 发送
// ---------------------------------------------------------------------------

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
	seq := this.seq.Add(1)
	m.Seq = seq

	// 本地执行
	hasLocal, localErr := this.execLocal(m)

	// 查订阅表
	this.l.Lock()
	peers := make([]string, 0, len(this.subs[en]))
	for name := range this.subs[en] {
		peers = append(peers, name)
	}
	this.l.Unlock()

	// EmitSomeOne 且本地有订阅者：本地最快，直接返回
	if t == msgtype.ReqRandOne && hasLocal {
		call.Err = localErr
		call.done()
		return
	}
	// 无远程 peer：直接聚合本地结果
	if len(peers) == 0 {
		call.Err = localErr
		call.done()
		return
	}

	nc := &nodeCall{
		call:      call,
		peerCount: uint64(len(peers)),
		targets:   peers,
		settled:   make(map[string]struct{}),
		localErr:  localErr,
		randOne:   t == msgtype.ReqRandOne,
	}
	this.l.Lock()
	this.pending[seq] = nc
	this.l.Unlock()
	for _, name := range peers {
		if err := this.writeToPeer(name, m); err != nil {
			// 投递失败：这个 peer 永远不会回包，立即结算
			this.resolve(name, &msg.Msg{T: msgtype.Res, Seq: seq, Err: err.Error()})
		}
	}
	return
}

// ---------------------------------------------------------------------------
// 写入
// ---------------------------------------------------------------------------

func (this *Client) writeToPeer(name string, m *msg.Msg) error {
	this.l.Lock()
	c := this.peers[name]
	this.l.Unlock()
	if c == nil {
		return fmt.Errorf("%w peer %s not found", ErrClient, name)
	}
	return this.writeConn(c, m)
}

func (this *Client) writeConn(c *Conn, m *msg.Msg) error {
	body, err := coder.Encode(coder.Msgp, nil, m)
	if err != nil {
		return fmt.Errorf("%w, err:%w", ErrClient, err)
	}
	return c.Write(body)
}

// ---------------------------------------------------------------------------
// 关闭
// ---------------------------------------------------------------------------

func (this *Client) Close() error {
	return this.CloseWithErr(fmt.Errorf("%w shutdown", ErrClient))
}

func (this *Client) CloseWithErr(err error) error {
	if !this.closed.CompareAndSwap(false, true) {
		return nil
	}
	if err == nil {
		err = ErrClient
	}
	this.cancel()

	this.l.Lock()
	lns := this.lns
	this.lns = nil
	serverConn := this.serverConn
	this.serverConn = nil
	peers := this.peers
	this.peers = map[string]*Conn{}
	pending := this.pending
	this.pending = map[uint64]*nodeCall{}
	this.l.Unlock()

	for _, ln := range lns {
		_ = ln.Close()
	}
	if serverConn != nil {
		_ = serverConn.Close()
	}
	for _, c := range peers {
		_ = c.Close()
	}
	ce := fmt.Errorf("%w,%w client closed", ErrClient, err)
	for _, nc := range pending {
		nc.call.Err = ce
		nc.call.done()
	}
	return err
}

// ---------------------------------------------------------------------------
// 参数解析（与 Emit 传参一一对应）
// ---------------------------------------------------------------------------

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

func joinErrs(errs ...error) error {
	var nonNil []error
	for _, e := range errs {
		if e != nil {
			nonNil = append(nonNil, e)
		}
	}
	if len(nonNil) == 0 {
		return nil
	}
	return errors.Join(nonNil...)
}

// ---------------------------------------------------------------------------
// Handler 实现
// ---------------------------------------------------------------------------

// serverClientHandler 节点到服务端的连接，处理 RegisterRes / NodeDown。
type serverClientHandler struct {
	c    *Client
	conn *Conn
}

func (this *serverClientHandler) HandleMsg(body []byte) error {
	var frame msg.Msg
	if _, err := coder.Decode(coder.Msgp, body, &frame); err != nil {
		return fmt.Errorf("%w,decode dir msg err:%w", ErrClientDecode, err)
	}
	switch frame.T {
	case msgtype.RegisterRes:
		if frame.Err != "" {
			slog.Error("register rejected", "err", frame.Err)
			return nil
		}
		var list []nodeInfo
		if len(frame.Bytes) > 0 {
			if _, err := coder.Decode(coder.Msgp, frame.Bytes, &list); err != nil {
				return fmt.Errorf("%w,decode node list err:%w", ErrClientDecode, err)
			}
		}
		this.c.onRegisterRes(list)
	case msgtype.NodeDown:
		this.c.onNodeDown(frame.Name)
	default:
		slog.Info("dir invalid msg", "msg", frame)
	}
	return nil
}

// peerHandler 节点之间的连接，处理订阅广播与事件请求/响应。
type peerHandler struct {
	c    *Client
	name string // 这条连接对端的节点名
	conn *Conn
}

func (this *peerHandler) HandleMsg(body []byte) error {
	var frame msg.Msg
	if _, err := coder.Decode(coder.Msgp, body, &frame); err != nil {
		return fmt.Errorf("%w,decode peer msg err:%w", ErrClientDecode, err)
	}
	switch frame.T {
	case msgtype.Subscribe:
		this.c.onSubscribe(this.name, frame.EventName)
	case msgtype.Unsubscribe:
		this.c.onUnsubscribe(this.name, frame.EventName)
	case msgtype.Req, msgtype.ReqRandOne:
		go this.c.func_call(this.name, &frame)
	case msgtype.Res, msgtype.ResRandOne:
		this.c.resolve(this.name, &frame)
	default:
		slog.Info("peer invalid msg", "name", this.name, "msg", frame)
	}
	return nil
}
