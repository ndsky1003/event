package event

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ndsky1003/event/v3/coder"
	"github.com/ndsky1003/event/v3/msg"
	"github.com/ndsky1003/event/v3/msgtype"
)

// nodeInfo 服务端下发给节点的节点信息（名字 + 监听地址列表）。
// 用 msgpack 反射编解码即可，无需 codegen。
type nodeInfo struct {
	Name  string   `msgpack:"n"`
	Addrs []string `msgpack:"a"`
}

// listenAddr 按地址前缀监听：unix:// 走 Unix domain socket（同机），其余走 TCP。
// Close 时自动 unlink socket 文件，避免重启后 bind: address already in use。
func listenAddr(addr string) (net.Listener, error) {
	if path, ok := strings.CutPrefix(addr, "unix://"); ok {
		_ = os.Remove(path)
		l, err := net.Listen("unix", path)
		if err != nil {
			return nil, err
		}
		if ul, ok := l.(*net.UnixListener); ok {
			ul.SetUnlinkOnClose(true)
		}
		return l, nil
	}
	return net.Listen("tcp", addr)
}

// dialAddr 按地址前缀拨号：unix:// 走 Unix domain socket，其余走 TCP。
func dialAddr(addr string) (net.Conn, error) {
	if path, ok := strings.CutPrefix(addr, "unix://"); ok {
		return net.Dial("unix", path)
	}
	return net.Dial("tcp", addr)
}

// serverSideVerify 服务端侧握手：读 Verify 请求，校验 secret，回 VerifyRes，返回对方 name。
// 只在 Serve() 之前调用（同步读写）。
func serverSideVerify(conn *Conn, secret string) (name string, err error) {
	body, err := conn.Read(5 * time.Second)
	if err != nil {
		return "", fmt.Errorf("%w,read verify req:%w", ErrServer, err)
	}
	var req msg.MsgVerifyReq
	if _, err = coder.Decode(coder.Msgp, body, &req); err != nil {
		return "", fmt.Errorf("%w,decode verify req:%w", ErrServer, err)
	}
	res := &msg.MsgVerifyRes{}
	if secret != "" && req.Secret != secret {
		res.Err = fmt.Errorf("%w,invalid secret", ErrServer).Error()
		_ = writeVerifyRes(conn, res)
		return "", fmt.Errorf("%w,invalid secret,name:%s", ErrServer, req.Name)
	}
	if err = writeVerifyRes(conn, res); err != nil {
		return "", err
	}
	return req.Name, nil
}

func writeVerifyRes(conn *Conn, res *msg.MsgVerifyRes) error {
	body, err := coder.Encode(coder.Msgp, nil, res)
	if err != nil {
		return fmt.Errorf("%w,encode verify res:%w", ErrServer, err)
	}
	if err = conn.WriteToConn(5*time.Second, body); err != nil {
		return fmt.Errorf("%w,write verify res:%w", ErrServer, err)
	}
	return nil
}

// clientSideVerify 客户端侧握手：发 Verify，读 VerifyRes。
func clientSideVerify(conn *Conn, name, secret string) error {
	req := &msg.MsgVerifyReq{Name: name, Secret: secret}
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

// connOptions 把服务端配置映射到传输层参数
func (this *ServerOption) connOptions() *connOption {
	o := ConnOptions()
	if this.heart_interval != nil {
		o.SetHeartInterval(*this.heart_interval)
	}
	return o
}

// Server p2p 服务端节点：只做节点注册、列表下发、下线通知，
// 不参与事件路由（不处理 Req/Res/Subscribe）。
type Server struct {
	opt    *ServerOption
	l      sync.Mutex
	nodes  map[string]*serverNode
	byConn map[*Conn]string
	ln     net.Listener
	closed atomic.Bool
	wg     sync.WaitGroup
}

type serverNode struct {
	name  string
	addrs []string
	conn  *Conn
}

func NewServer(opts ...*ServerOption) *Server {
	d := &Server{
		nodes:  map[string]*serverNode{},
		byConn: map[*Conn]string{},
	}
	d.opt = ServerOptions().
		SetSecret("").
		SetHeartInterval(20 * time.Second).
		SetIsWrapError(true).
		merges(opts...)
	return d
}

// Listen 监听并阻塞，直到 Shutdown。地址规则同 listenAddr（unix:// 前缀走 UDS）。
func (this *Server) Listen(addr string) {
	if this == nil {
		panic("server is nil")
	}
	if this.closed.Load() {
		slog.Error("server already shutdown, skip listen", "addr", addr)
		return
	}
	ln, err := listenAddr(addr)
	if err != nil {
		panic(err)
	}
	this.l.Lock()
	if this.closed.Load() {
		this.l.Unlock()
		_ = ln.Close()
		return
	}
	this.ln = ln
	this.l.Unlock()
	slog.Info("event server listening", "addr", addr)

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
		this.wg.Add(1)
		go func() {
			defer this.wg.Done()
			this.handleConn(conn)
		}()
	}
}

// Shutdown 关闭服务端：停止监听、断开所有节点连接。幂等。
func (this *Server) Shutdown() error {
	if !this.closed.CompareAndSwap(false, true) {
		return nil
	}
	this.l.Lock()
	ln := this.ln
	this.ln = nil
	conns := make([]*Conn, 0, len(this.nodes))
	for _, dn := range this.nodes {
		conns = append(conns, dn.conn)
	}
	this.l.Unlock()

	var errs []error
	if ln != nil {
		if err := ln.Close(); err != nil {
			errs = append(errs, fmt.Errorf("%w,close listener err:%w", ErrServer, err))
		}
	}
	for _, c := range conns {
		_ = c.Close()
	}
	this.wg.Wait()
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (this *Server) IsShutdown() bool {
	return this.closed.Load()
}

// handleConn 一条节点连接的完整生命周期：握手 → 收 Register → 跑连接 → 清理 + 广播下线。
func (this *Server) handleConn(raw net.Conn) {
	conn := New(context.Background(), raw, nil, this.opt.connOptions())

	// 握手确定 name
	name, err := serverSideVerify(conn, *this.opt.secret)
	if err != nil {
		slog.Error("server handshake", "err", err)
		_ = conn.Close()
		return
	}
	conn.SetHandler(&serverHandler{d: this, conn: conn, name: name})

	if err := conn.Serve(); err != nil && !errors.Is(err, ErrConnClosed) {
		slog.Error("server conn die", "name", name, "err", err)
	}

	// 连接结束：清理节点并广播下线
	this.removeNode(name)
}

// register 处理节点的 Register 消息：登记节点，回发当前节点列表。
func (this *Server) register(conn *Conn, frame *msg.Msg) {
	name := frame.Name
	var addrs []string
	if len(frame.Bytes) > 0 {
		if _, err := coder.Decode(coder.Msgp, frame.Bytes, &addrs); err != nil {
			slog.Error("server decode addrs", "name", name, "err", err)
		}
	}
	this.l.Lock()
	if name == "" {
		this.l.Unlock()
		this.write(conn, &msg.Msg{T: msgtype.RegisterRes, Err: "empty node name"})
		_ = conn.Close()
		return
	}
	if _, exists := this.nodes[name]; exists {
		this.l.Unlock()
		slog.Error("server duplicate node name", "name", name)
		this.write(conn, &msg.Msg{T: msgtype.RegisterRes, Err: "duplicate node name"})
		_ = conn.Close()
		return
	}
	// 当前已有节点列表（排除自己）
	list := make([]nodeInfo, 0, len(this.nodes))
	for n, dn := range this.nodes {
		if n != name {
			list = append(list, nodeInfo{Name: n, Addrs: dn.addrs})
		}
	}
	this.nodes[name] = &serverNode{name: name, addrs: addrs, conn: conn}
	this.byConn[conn] = name
	this.l.Unlock()

	body, err := coder.Encode(coder.Msgp, nil, list)
	if err != nil {
		slog.Error("server encode node list", "err", err)
		_ = conn.Close()
		return
	}
	this.write(conn, &msg.Msg{T: msgtype.RegisterRes, Bytes: body})
	slog.Info("server node register", "name", name, "addrs", addrs)
}

// removeNode 节点下线：从登记里移除，并广播 NodeDown 给其他节点。
func (this *Server) removeNode(name string) {
	this.l.Lock()
	dn, ok := this.nodes[name]
	if !ok {
		this.l.Unlock()
		return
	}
	delete(this.nodes, name)
	if dn.conn != nil {
		delete(this.byConn, dn.conn)
	}
	// 广播给其他节点
	conns := make([]*Conn, 0, len(this.nodes))
	for _, x := range this.nodes {
		conns = append(conns, x.conn)
	}
	this.l.Unlock()

	m := &msg.Msg{T: msgtype.NodeDown, Name: name}
	for _, c := range conns {
		this.write(c, m)
	}
	slog.Info("server node down", "name", name)
}

func (this *Server) write(conn *Conn, m *msg.Msg) {
	body, err := coder.Encode(coder.Msgp, nil, m)
	if err != nil {
		slog.Error("server encode msg", "err", err)
		return
	}
	if err := conn.Write(body); err != nil {
		slog.Error("server write", "err", err)
	}
}

// serverHandler 服务端连接的 Handler，只处理 Register。
type serverHandler struct {
	d    *Server
	conn *Conn
	name string
}

func (this *serverHandler) HandleMsg(body []byte) error {
	var frame msg.Msg
	if _, err := coder.Decode(coder.Msgp, body, &frame); err != nil {
		return fmt.Errorf("%w,decode body err:%w", ErrServer, err)
	}
	switch frame.T {
	case msgtype.Register:
		this.d.register(this.conn, &frame)
	default:
		slog.Info("server invalid msg", "name", this.name, "msg", frame)
	}
	return nil
}
