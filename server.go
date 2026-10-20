package event

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/antlabs/timer"
	"github.com/ndsky1003/event/v2/eventname"
	"github.com/ndsky1003/event/v2/msg"
	"github.com/ndsky1003/event/v2/msgtype"
	"github.com/ndsky1003/event/v2/topic"
	"github.com/sirupsen/logrus"
)

// topicSids 一个 topic 条目加上它的监听者集合。
// exact / regs 里存的是这个指针，service.tps 也指向同一个对象，
// 所以连接断开时只摘掉自己注册过的那几条，不必遍历全部 topic。
type topicSids struct {
	tp   *topic.Topic
	sids map[uint32]struct{}
}

type server struct {
	sid uint32
	opt *ServerOption
	seq uint64
	tm  timer.Timer //时间轮
	l   sync.Mutex  //protect under: exact / regs / services / pending / service.tps / listeners
	// listeners 必须存下来：不存就关不掉，listen() 里的 Accept 错误分支
	// 就成了永久忙循环，而且进程也退不出去。
	listeners []net.Listener
	// acceptWg 数 accept 循环，connWg 数已接进来的连接处理。
	// 分开是因为 Shutdown 必须"先等 accept 退出、再断连接"，
	// 合成一个就会自己等自己（连接没断，handle_conn 就不会返回）。
	acceptWg sync.WaitGroup
	connWg   sync.WaitGroup
	closed   atomic.Bool
	// 普通 topic：事件名字符串相等，直接查表
	exact map[eventname.T]*topicSids
	// 正则 topic：字面量做 key 去重，匹配只能线性扫
	regs     map[eventname.T]*topicSids
	services map[uint32]*service
	pending  map[uint64]*server_call //server to client 调用的call
}

type server_call struct {
	tn         timer.TimeNoder //超时检测,如期返回就stop
	origin_sid uint32          //原始的sid
	origin_seq uint64          // frame.需要改成服务器的seq

	serverReqCount uint64
	// targets 这次请求实际投递给了哪些监听者。release_timeout 收尾时用它
	// 把 service.inflight 里的残留清掉。
	targets []uint32
	// settled 已经结算过的监听者。没有这张表，同一个 sid 被结算两次
	// （先收到 Res、之后连接又断了）就会把 uint64 计数减成负数，
	// pending 永远等不到 0，只能靠 Timeout 收尾。
	settled map[uint32]struct{}
	errs    []error
}

// hasTarget 这次请求是否投递给了 sid。
// targets 通常只有几个（一个 topic 的监听者数量），线性扫比塞一张 map 便宜。
func (this *server_call) hasTarget(sid uint32) bool {
	for _, x := range this.targets {
		if x == sid {
			return true
		}
	}
	return false
}

// pendingWrite 一条需要在锁外发出去的消息
type pendingWrite struct {
	sid uint32
	m   *msg.Msg
}

func NewServer(opts ...*ServerOption) *server {
	c := &server{
		tm:       timer.NewTimer(),
		services: map[uint32]*service{},
		exact:    map[eventname.T]*topicSids{},
		regs:     map[eventname.T]*topicSids{},
		pending:  map[uint64]*server_call{},
	}
	c.opt = ServerOptions().
		SetSecret("").
		SetTimeout(10 * time.Second).
		SetHeartInterval(20 * time.Second).
		SetIsWrapError(true).
		merges(opts...)
	go c.tm.Run()
	return c
}

// connOptions 把服务端配置映射到传输层参数
func (this *ServerOption) connOptions() *connOption {
	o := ConnOptions()
	if this.heart_interval != nil {
		o.SetHeartInterval(*this.heart_interval)
	}
	return o
}

// addrs ["192.168.0.1:8080","192.168.0.2:8080"]
// 最后一个地址阻塞（保持原有语义），前面的各起一个 goroutine。
// 想让阻塞的那个返回，就调 Shutdown。
func (this *server) Listens(addrs []string) {
	for i := len(addrs) - 1; i >= 0; i-- {
		addr := addrs[i]
		if i != 0 {
			go this.listen(addr)
		} else {
			this.listen(addr)
		}
	}
}

//url:port
func (this *server) Listen(url string) {
	this.listen(url)
}

func (this *server) listen(url string) {
	if this == nil {
		panic("server is nil")
	}
	this.acceptWg.Add(1)
	defer this.acceptWg.Done()

	if this.closed.Load() {
		logrus.Errorf("%v,server already shutdown,skip listen:%v", ErrServer, url)
		return
	}
	listen, err := net.Listen("tcp", url)
	if err != nil {
		panic(err)
	}
	if !this.add_listener(listen) {
		// Shutdown 正好落在 net.Listen 和登记之间，listener 已被就地关掉
		return
	}
	logrus.Infof("event server listening on %s", url)

	for {
		conn, err := listen.Accept()
		if err != nil {
			// Shutdown 之后 Accept 必然报错，这是正常退出路径，不是异常
			if this.closed.Load() {
				return
			}
			logrus.Error(fmt.Errorf("%w,accept err:%w", ErrServer, err))
			// 不是 Shutdown 引起的 Accept 错误（fd 耗尽、临时资源不足之类）：
			// 原来这里是裸 continue，等于忙循环，一瞬间能刷出几十万行日志。
			time.Sleep(100 * time.Millisecond)
			continue
		}
		this.connWg.Add(1)
		go func() {
			defer this.connWg.Done()
			this.handle_conn(conn)
		}()
	}
}

// add_listener 登记 listener。返回 false 表示服务端已经 Shutdown，
// 这个 listener 已被就地关掉，调用方应当直接退出。
func (this *server) add_listener(l net.Listener) bool {
	this.l.Lock()
	if this.closed.Load() {
		this.l.Unlock()
		if err := l.Close(); err != nil {
			logrus.Errorf("%v,close listener err:%v", ErrServer, err)
		}
		return false
	}
	this.listeners = append(this.listeners, l)
	this.l.Unlock()
	return true
}

// Shutdown 关掉服务端：停止监听 → 断开所有客户端连接 → 停掉时间轮，
// 并等已接进来的连接处理干净。幂等。
//
// 阻塞在 Listen / Listens 里的调用会因为 Accept 返回错误而返回。
// 不调 Shutdown 的话，accept 循环和 timer.Run 的 goroutine 都没有退出的一天，
// 进程只能靠 main 返回或 os.Exit 收尾。
func (this *server) Shutdown() error {
	if !this.closed.CompareAndSwap(false, true) {
		return nil
	}

	this.l.Lock()
	listeners := this.listeners
	this.listeners = nil
	sids := make([]uint32, 0, len(this.services))
	for sid := range this.services {
		sids = append(sids, sid)
	}
	this.l.Unlock()

	var errs []error
	for _, l := range listeners {
		if err := l.Close(); err != nil {
			errs = append(errs, fmt.Errorf("%w,close listener err:%w", ErrServer, err))
		}
	}
	// 先等 accept 循环退出，之后才不会有新连接进来
	this.acceptWg.Wait()

	// 再断开所有客户端：连接一关，对端的 readPump 就退出，
	// 它们本地挂着的 Call 会被 disconnect 结算掉。
	// 顺带把这条连接欠着的响应结算掉（closeLocked 里做）。
	for _, sid := range sids {
		if err := this.Close(sid, true); err != nil {
			errs = append(errs, err)
		}
	}
	this.connWg.Wait()

	this.tm.Stop()

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// IsShutdown 服务端是否已经关掉
func (this *server) IsShutdown() bool {
	return this.closed.Load()
}

// handle_conn 一条客户端连接的完整生命周期：握手 → 注册 → 跑读写泵 → 清理。
// 阻塞直到连接结束，所以清理一定发生，不会漏 service。
func (this *server) handle_conn(raw net.Conn) {
	sid := atomic.AddUint32(&this.sid, 1)

	conn, err := New(raw, nil, this.opt.connOptions())
	if err != nil {
		err = fmt.Errorf("%w,new conn err:%w", ErrServer, err)
		if e := raw.Close(); e != nil {
			err = fmt.Errorf("%w,%w", err, e)
		}
		logrus.Error(err)
		return
	}
	s := newService(this, sid, conn)
	conn.SetHandler(s)

	if err = s.handshake(); err != nil {
		logrus.Error(err)
		if e := conn.Close(); e != nil {
			logrus.Errorf("%v,close conn err:%v", ErrServer, e)
		}
		return
	}

	this.l.Lock()
	if this.closed.Load() {
		// Shutdown 正好落在握手和登记之间：别把自己挂上去，直接收场
		this.l.Unlock()
		if e := conn.Close(); e != nil {
			logrus.Errorf("%v,close conn err:%v", ErrServer, e)
		}
		return
	}
	this.services[sid] = s
	this.l.Unlock()
	logrus.Infof("event service:%s[%d] is ready", s.name, sid)

	// Serve 阻塞到 readPump/writePump 都退出；心跳与断连检测由 Conn 负责
	if err = conn.Serve(); err != nil && !errors.Is(err, ErrConnClosed) {
		logrus.Errorf("service id:%d is die end,err:%v", sid, err)
	}
	// 连接结束：把自己从 topic/services 里摘掉，并结算它欠着的响应。
	// 传 false 是因为连接已经关了，不需要再关一次。
	this.Close(sid, false)
	logrus.Infof("event service:%s[%d] exit", s.name, sid)
}

func (this *server) Close(sid uint32, isCloseSon bool) error {
	this.l.Lock()
	writes, err := this.closeLocked(sid, isCloseSon)
	this.l.Unlock()

	// 写回必须在锁外：write 自己会拿 this.l
	for _, w := range writes {
		this.write(w.sid, w.m)
	}
	return err
}

// closeLocked 摘掉一条连接。必须在持 this.l 时调用，返回需要锁外发出的消息。
func (this *server) closeLocked(sid uint32, isCloseSon bool) (writes []pendingWrite, err error) {
	s, ok := this.services[sid]
	if !ok {
		return
	}
	delete(this.services, sid)

	// 只摘自己注册过的 topic，不用遍历整个 exact/regs
	for _, ts := range s.tps {
		delete(ts.sids, sid)
	}
	s.tps = nil

	// 它作为监听者参与、还没回包的请求：现在永远不会回包了。
	// 不结算的话发布者只能干等 Timeout（默认 10s）。
	//
	// 这里扫全部 pending，而不是给每条连接维护一个"我欠着哪些 seq"的表：
	// 断线是罕见路径，请求是热路径。per-request 维护那张表实测要付约 7% 吞吐
	// （每个监听者两次 map 写），扫 pending 只在断线时付 O(pending × targets)。
	e := fmt.Errorf("%w,service id:%d connection lost before response", ErrServer, sid)
	for seq, s_call := range this.pending {
		if !s_call.hasTarget(sid) {
			continue
		}
		if w := this.settleLocked(sid, s.name, &msg.Msg{
			T:   msgtype.Res,
			Seq: seq,
			Err: e.Error(),
		}); w != nil {
			writes = append(writes, *w)
		}
	}

	if isCloseSon {
		err = s.Close()
	}
	return
}

func (this *server) handle(sid uint32, sname string, frame *msg.Msg) {
	switch frame.T {
	case msgtype.On:
		this.on(sid, frame)
	case msgtype.Req, msgtype.ReqRandOne:
		this.req(sid, frame)
	case msgtype.Res, msgtype.ResRandOne:
		this.settle(sid, sname, frame)
	default:
		logrus.Infof("丢弃：%d,name:%v,msg:%+v\n", sid, sname, frame)
	}

}

func (this *server) on(sid uint32, frame *msg.Msg) {
	en := frame.EventName
	tp := topic.New(en)
	this.l.Lock()
	var ts *topicSids
	if tp.IsReg {
		ts = this.regs[en]
	} else {
		ts = this.exact[en]
	}
	if ts == nil {
		ts = &topicSids{tp: tp, sids: map[uint32]struct{}{}}
		if tp.IsReg {
			this.regs[en] = ts
		} else {
			this.exact[en] = ts
		}
	}
	ts.sids[sid] = struct{}{}
	if s, ok := this.services[sid]; ok {
		s.addTopicLocked(ts)
	}
	this.l.Unlock()
	this.write(sid, frame)
}

// matchLocked 找出监听 et 的所有 topic 条目。
// 普通 topic 是字符串相等，直接查表命中；只有正则必须线性扫。
// 旧实现是把所有 topic 都 Match 一遍（代码自注"性能损耗极其严重"），
// 现在锁内的工作量只剩"一次查表 + 正则条数"。
func (this *server) matchLocked(et eventname.T, fn func(ts *topicSids)) {
	if ts, ok := this.exact[et]; ok {
		fn(ts)
	}
	for _, ts := range this.regs {
		if ts.tp.Match(et) {
			fn(ts)
		}
	}
}

func (this *server) req(sid uint32, frame *msg.Msg) {
	et := frame.EventName
	server_seq := atomic.AddUint64(&this.seq, 1)
	origin_seq := frame.Seq
	frame.Seq = server_seq
	s_call := &server_call{
		origin_sid: sid,
		origin_seq: origin_seq,
		settled:    make(map[uint32]struct{}),
	}
	var hasSendServiceID = map[uint32]struct{}{}
	this.l.Lock()
	this.pending[server_seq] = s_call
	this.matchLocked(et, func(ts *topicSids) {
		for lsid := range ts.sids {
			if _, ok := hasSendServiceID[lsid]; ok {
				continue
			}
			hasSendServiceID[lsid] = struct{}{}
			s_call.serverReqCount++
			// 记下投递给了谁：连接半路死掉时 closeLocked 扫 pending 要用它收尾
			s_call.targets = append(s_call.targets, lsid)
		}
	})
	isDone := s_call.serverReqCount > 0
	if isDone {
		// 定时器必须在锁内创建：res() 会在锁内读 s_call.tn 并 Stop，
		// 锁外赋值与之竞争（TODO.md 第 2、3 条）。
		s_call.tn = this.tm.AfterFunc(*this.opt.Timeout, func() {
			this.release_timeout(server_seq)
		})
	}
	this.l.Unlock()

	for lsid := range hasSendServiceID {
		if err := this.write(lsid, frame); err != nil {
			// 投递失败意味着这个监听者永远不会回包。不补偿计数的话，
			// pending 只能等到 Timeout（默认 10s）才释放，发布者白等 10 秒
			// ——实测：监听者 Stop 后紧接着 Emit，耗时 10.009s 才拿到 timeout。
			this.dispatch_failed(lsid, server_seq, err)
		}
	}
	if !isDone {
		frame.T = msgtype.Res
		frame.Seq = s_call.origin_seq
		frame.Bytes = nil
		frame.BodyCount = 0
		this.l.Lock()
		delete(this.pending, server_seq)
		this.l.Unlock()
		this.write(sid, frame)
	}
}

// dispatch_failed 处理"投递失败、这个监听者永远不会回包"的情况：
// 直接复用 settle 的计数递减与错误聚合，减到 0 就把结果写回发布者，
// 不必再等 Timeout。
func (this *server) dispatch_failed(sid uint32, server_seq uint64, e error) {
	this.settle(sid, this.service_name(sid), &msg.Msg{
		T:   msgtype.Res,
		Seq: server_seq,
		Err: e.Error(),
	})
}

// settle 结算一个监听者的响应（或"它永远不会回包"这件事）。
func (this *server) settle(sid uint32, serviceName string, m *msg.Msg) {
	this.l.Lock()
	w := this.settleLocked(sid, serviceName, m)
	this.l.Unlock()

	if w != nil {
		this.write(w.sid, w.m)
	}
}

// settleLocked 必须在持 this.l 时调用。返回 nil 表示这次不用写回发布者。
func (this *server) settleLocked(sid uint32, serviceName string, m *msg.Msg) *pendingWrite {
	server_seq := m.Seq
	s_call, ok := this.pending[server_seq]
	if !ok {
		return nil
	}
	if _, dup := s_call.settled[sid]; dup {
		// 这个监听者已经结算过（比如先收到 Res、之后连接又断了）。
		// 再减一次计数就下溢成天文数字，pending 再也等不到 0。
		return nil
	}
	s_call.settled[sid] = struct{}{}
	s_call.serverReqCount--
	leftCount := s_call.serverReqCount
	if e := m.Err; e != "" {
		var ee error
		if *this.opt.is_wrap_error {
			ee = fmt.Errorf("Name:[%s],Err:[%s]", serviceName, e)
		} else {
			ee = errors.New(e)
		}
		s_call.errs = append(s_call.errs, ee)
	}
	// 是否在锁外写回，必须在锁内用局部变量决定：
	// 锁外再读 s_call.serverReqCount 会与其它 goroutine 的 -- 竞争，
	// 导致重复写回、或把没聚合完整的错误先发出去（TODO.md 第 1 条）。
	if leftCount != 0 && m.T != msgtype.ResRandOne {
		return nil
	}
	if len(s_call.errs) > 0 {
		m.Err = errors.Join(s_call.errs...).Error()
	}
	m.Seq = s_call.origin_seq
	if s_call.tn != nil {
		s_call.tn.Stop()
	}
	delete(this.pending, server_seq)
	return &pendingWrite{sid: s_call.origin_sid, m: m}
}

// write 把消息投递给 sid，并把投递错误回给调用方。
// 连接收尾由 handle_conn 负责，这里只负责"送没送出去"。
// 返回值必须被关心 pending 计数的调用方（req）使用，其余调用方可以忽略。
func (this *server) write(sid uint32, m *msg.Msg) error {
	// Shutdown 之后不再投递：一是没意义，二是断连接时 closeLocked 会结算一批
	// 欠着的响应，那些回包正好都要 write 出去，不在这里挡住就会刷一堆错误日志。
	if this.closed.Load() {
		return fmt.Errorf("%w,server shutdown", ErrServer)
	}
	this.l.Lock()
	s, ok := this.services[sid]
	this.l.Unlock()
	if !ok {
		return fmt.Errorf("%w,service id:%d not found", ErrServer, sid)
	}
	if err := s.Write(m); err != nil {
		logrus.Error(err)
		return err
	}
	return nil
}

// service_name 取连接名字，取不到说明已经被摘掉了
func (this *server) service_name(sid uint32) string {
	this.l.Lock()
	defer this.l.Unlock()
	if s, ok := this.services[sid]; ok {
		return s.name
	}
	return ""
}

func (this *server) release_timeout(server_seq uint64) {
	this.l.Lock()
	s_call, ok := this.pending[server_seq]
	if ok {
		if s_call.tn != nil {
			s_call.tn.Stop()
		}
		delete(this.pending, server_seq)
	}
	this.l.Unlock()

	if ok {
		frame := &msg.Msg{
			T:   msgtype.Res,
			Seq: s_call.origin_seq,
			Err: fmt.Errorf("%w,timeout", ErrServer).Error(),
		}
		this.write(s_call.origin_sid, frame)
	}
}
