package event

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndsky1003/event/v3/eventname"
	"github.com/ndsky1003/event/v3/msgtype"
)

// 端到端集成测试：p2p 模式。服务端节点只做发现，节点之间订阅广播 + 点对点事件路由。

func startServer(t *testing.T, opts ...*ServerOption) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	dir := NewServer(opts...)
	go dir.Listen(addr)
	t.Cleanup(func() {
		if err := dir.Shutdown(); err != nil {
			t.Logf("Shutdown: %v", err)
		}
	})
	return addr
}

func startNode(t *testing.T, serverAddr, name string, opts ...*ClientOption) *Client {
	t.Helper()
	full := append([]*ClientOption{}, opts...)
	// :0 让库自己分配随机端口（监听成功后回填实际地址注册到服务端）
	full = append(full, ClientOptions().SetName(name).SetListenAddr("127.0.0.1:0"))
	c := Dial(serverAddr, full...)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待超时(%v): %s", timeout, what)
}

func isConnected(c *Client) bool {
	c.l.Lock()
	defer c.l.Unlock()
	return c.serverConn != nil
}

func peerCount(c *Client) int {
	c.l.Lock()
	defer c.l.Unlock()
	return len(c.peers)
}

func subCount(c *Client, en eventname.T) int {
	c.l.Lock()
	defer c.l.Unlock()
	return len(c.subs[en])
}

func serverNodeCount(d *Server) int {
	d.l.Lock()
	defer d.l.Unlock()
	return len(d.nodes)
}

// 等两个节点互相连上，并且 sub 的订阅同步到了 pub。
func waitRoute(t *testing.T, sub, pub *Client, en eventname.T) {
	t.Helper()
	waitFor(t, 10*time.Second, "节点互连", func() bool {
		return peerCount(sub) == 1 && peerCount(pub) == 1
	})
	waitFor(t, 10*time.Second, "订阅同步", func() bool {
		return subCount(pub, en) == 1
	})
}

func TestEmitOnRoundTrip(t *testing.T) {
	addr := startServer(t)
	sub := startNode(t, addr, "sub", ClientOptions().SetHeartInterval(time.Second))
	pub := startNode(t, addr, "pub", ClientOptions().SetHeartInterval(time.Second))

	got := make(chan string, 8)
	if err := sub.On("itest.echo", func(name string) error {
		got <- name
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	waitRoute(t, sub, pub, "itest.echo")

	if err := pub.Emit("itest.echo", "hello"); err != nil {
		t.Fatalf("Emit 报错: %v", err)
	}
	select {
	case v := <-got:
		if v != "hello" {
			t.Fatalf("收到意外内容: %q", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("事件没有送达")
	}
}

func TestHandlerErrorPropagatesToEmitter(t *testing.T) {
	addr := startServer(t)
	sub := startNode(t, addr, "sub", ClientOptions().SetHeartInterval(time.Second))
	pub := startNode(t, addr, "pub", ClientOptions().SetHeartInterval(time.Second))

	if err := sub.On("itest.fail", func(s string) error {
		return errors.New("handler boom")
	}); err != nil {
		t.Fatal(err)
	}
	waitRoute(t, sub, pub, "itest.fail")

	err := pub.Emit("itest.fail", "x")
	if err == nil || !strings.Contains(err.Error(), "handler boom") {
		t.Fatalf("handler 的错误没有回传: %v", err)
	}
	t.Logf("错误已回传: %v", err)
}

// 多个订阅节点，错误要全部聚合回发布者。
func TestMultipleListenersErrorsAggregated(t *testing.T) {
	addr := startServer(t)
	const n = 3
	pub := startNode(t, addr, "pub", ClientOptions().SetHeartInterval(time.Second))

	for i := 0; i < n; i++ {
		idx := i
		sub := startNode(t, addr, fmt.Sprintf("sub%d", idx), ClientOptions().SetHeartInterval(time.Second))
		if err := sub.On("itest.multi", func(s string) error {
			return fmt.Errorf("boom-%d", idx)
		}); err != nil {
			t.Fatal(err)
		}
	}

	waitFor(t, 15*time.Second, "三个订阅同步", func() bool {
		return subCount(pub, "itest.multi") == n
	})

	err := pub.Emit("itest.multi", "x")
	if err == nil {
		t.Fatal("应该聚合到错误，结果 err=nil")
	}
	for j := 0; j < n; j++ {
		if !strings.Contains(err.Error(), fmt.Sprintf("boom-%d", j)) {
			t.Fatalf("缺少第 %d 个错误: %v", j, err)
		}
	}
	t.Logf("聚合到全部 %d 个错误", n)
}

// EmitSomeOne 只等最快的响应。
func TestEmitSomeOne(t *testing.T) {
	addr := startServer(t)
	pub := startNode(t, addr, "pub", ClientOptions().SetHeartInterval(time.Second))

	speeds := []time.Duration{200 * time.Millisecond, 10 * time.Millisecond, 100 * time.Millisecond}
	for i, d := range speeds {
		idx := i
		dd := d
		sub := startNode(t, addr, fmt.Sprintf("sub%d", idx), ClientOptions().SetHeartInterval(time.Second))
		if err := sub.On("itest.rand", func() error {
			time.Sleep(dd)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 15*time.Second, "订阅同步", func() bool {
		return subCount(pub, "itest.rand") == len(speeds)
	})

	start := time.Now()
	if err := pub.EmitSomeOne("itest.rand"); err != nil {
		t.Fatalf("EmitSomeOne 报错: %v", err)
	}
	cost := time.Since(start)
	if cost > 150*time.Millisecond {
		t.Fatalf("EmitSomeOne 应该等最快的（~10ms），实际 %v", cost.Round(time.Millisecond))
	}
	t.Logf("EmitSomeOne 耗时 %v（最快 ~10ms）", cost.Round(time.Millisecond))
}

func TestEmitAsync(t *testing.T) {
	addr := startServer(t)
	sub := startNode(t, addr, "sub", ClientOptions().SetHeartInterval(time.Second))
	pub := startNode(t, addr, "pub", ClientOptions().SetHeartInterval(time.Second))

	got := make(chan int, 8)
	if err := sub.On("itest.async", func(n int) error { got <- n; return nil }); err != nil {
		t.Fatal(err)
	}
	waitRoute(t, sub, pub, "itest.async")

	call := pub.EmitAsync("itest.async", 42)
	select {
	case c := <-call.Done:
		if c.Err != nil {
			t.Fatalf("EmitAsync 报错: %v", c.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EmitAsync 没有完成")
	}
	select {
	case v := <-got:
		if v != 42 {
			t.Fatalf("参数不对: %d", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("回调没触发")
	}
}

func TestEmitBatch(t *testing.T) {
	addr := startServer(t)
	sub := startNode(t, addr, "sub", ClientOptions().SetHeartInterval(time.Second))
	pub := startNode(t, addr, "pub", ClientOptions().SetHeartInterval(time.Second))

	got := make(chan string, 256)
	if err := sub.On("batch.test", func(s string) error { got <- s; return nil }); err != nil {
		t.Fatal(err)
	}
	waitRoute(t, sub, pub, "batch.test")

	const n = 100
	argsList := make([][]any, n)
	for i := range argsList {
		argsList[i] = []any{fmt.Sprintf("msg-%d", i)}
	}
	errs := pub.EmitBatch("batch.test", argsList...)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 条 Emit 失败: %v", i, err)
		}
	}
	for i := 0; i < n; i++ {
		select {
		case <-got:
		case <-time.After(5 * time.Second):
			t.Fatalf("只收到 %d 个回调，期望 %d", i, n)
		}
	}
}

// 空闲连接必须靠传输层心跳活下来，不能被判死。
func TestIdleConnSurvivesHeartbeat(t *testing.T) {
	addr := startServer(t)
	sub := startNode(t, addr, "sub", ClientOptions().SetHeartInterval(300*time.Millisecond))
	pub := startNode(t, addr, "pub", ClientOptions().SetHeartInterval(300*time.Millisecond))

	got := make(chan struct{}, 8)
	if err := sub.On("itest.idle", func(s string) error { got <- struct{}{}; return nil }); err != nil {
		t.Fatal(err)
	}
	waitRoute(t, sub, pub, "itest.idle")

	if err := pub.Emit("itest.idle", "warmup"); err != nil {
		t.Fatalf("warmup Emit 失败: %v", err)
	}
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("warmup 没送达")
	}

	time.Sleep(3 * time.Second)
	if peerCount(pub) != 1 {
		t.Fatal("空闲期间 peer 连接断了")
	}
	if err := pub.Emit("itest.idle", "after-idle"); err != nil {
		t.Fatalf("静默后 Emit 失败: %v", err)
	}
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("静默后事件没有送达")
	}
}

// peer 断线重连后，订阅关系要重新建立。
func TestReconnectAndResubscribe(t *testing.T) {
	addr := startServer(t)
	sub := startNode(t, addr, "sub", ClientOptions().SetHeartInterval(time.Second).SetCheckInterval(100*time.Millisecond))
	pub := startNode(t, addr, "pub", ClientOptions().SetHeartInterval(time.Second).SetCheckInterval(100*time.Millisecond))

	got := make(chan string, 16)
	if err := sub.On("itest.reconn", func(s string) error { got <- s; return nil }); err != nil {
		t.Fatal(err)
	}
	waitRoute(t, sub, pub, "itest.reconn")

	if err := pub.Emit("itest.reconn", "before"); err != nil {
		t.Fatalf("首条 Emit 失败: %v", err)
	}
	select {
	case v := <-got:
		if v != "before" {
			t.Fatalf("期望 before，实际 %q", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("首条没送达")
	}

	// 掐掉 pub 到 sub 的 peer 连接（pub 是主动连接方），触发 pub 重连
	pub.l.Lock()
	for _, c := range pub.peers {
		_ = c.Close()
	}
	pub.l.Unlock()

	waitFor(t, 15*time.Second, "重连后重新订阅并送达", func() bool {
		if peerCount(pub) != 1 {
			return false
		}
		if subCount(pub, "itest.reconn") != 1 {
			return false
		}
		if err := pub.Emit("itest.reconn", "after"); err != nil {
			return false
		}
		select {
		case v := <-got:
			return v == "after"
		default:
			return false
		}
	})
}

// 订阅节点收下请求后断线，发布节点的 pending 必须立即结算。
func TestListenerDeathReleasesPending(t *testing.T) {
	addr := startServer(t)
	sub := startNode(t, addr, "sub", ClientOptions().SetHeartInterval(time.Second).SetCheckInterval(10*time.Second))
	pub := startNode(t, addr, "pub", ClientOptions().SetHeartInterval(time.Second).SetCheckInterval(10*time.Second))

	started := make(chan struct{})
	if err := sub.On("itest.slow", func() error {
		close(started)
		time.Sleep(30 * time.Second)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	waitRoute(t, sub, pub, "itest.slow")

	call := pub.EmitAsync("itest.slow")
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("订阅节点没收到请求")
	}

	sub.Close()

	select {
	case c := <-call.Done:
		if c.Err == nil {
			t.Fatal("订阅节点断线后应该回错误，结果 err=nil")
		}
		if !strings.Contains(c.Err.Error(), "connection lost") {
			t.Logf("错误内容: %v", c.Err)
		}
		t.Logf("订阅节点断线立即结算: %v", c.Err)
	case <-time.After(5 * time.Second):
		t.Fatal("订阅节点欠的响应没有被断线收尾")
	}
}

// Close 之后不能偷偷重连。
func TestClientCloseStopsReconnect(t *testing.T) {
	addr := startServer(t)
	c := startNode(t, addr, "closer", ClientOptions().SetHeartInterval(time.Second).SetCheckInterval(200*time.Millisecond))
	waitFor(t, 10*time.Second, "连接服务端", func() bool { return isConnected(c) })

	if err := c.Close(); err != nil {
		t.Logf("Close 返回: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if isConnected(c) {
		t.Fatal("Close 之后 serverConn 仍非 nil")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("第二次 Close 应返回 nil，实际 %v", err)
	}
	_ = c.Emit("itest.after-close")
	if isConnected(c) {
		t.Fatal("Emit 之后又偷偷重连了")
	}
}

// 错误 secret 的节点无法注册到服务端。
func TestWrongSecretRejected(t *testing.T) {
	addr := startServer(t, ServerOptions().SetSecret("right").SetHeartInterval(time.Second))

	c := Dial(addr, ClientOptions().
		SetName("bad").SetSecret("wrong").
		SetListenAddr("127.0.0.1:0").
		SetHeartInterval(time.Second).
		SetCheckInterval(100*time.Millisecond))
	defer c.Close()

	time.Sleep(600 * time.Millisecond)
	if isConnected(c) {
		t.Fatal("错误 secret 下不应连上服务端")
	}
	t.Log("错误 secret 被拒")
}

func TestUnixSocket(t *testing.T) {
	dirSock := filepath.Join(t.TempDir(), "dir.sock")
	dir := NewServer(ServerOptions().SetHeartInterval(time.Second))
	go dir.Listen("unix://" + dirSock)
	t.Cleanup(func() { _ = dir.Shutdown() })

	subSock := filepath.Join(t.TempDir(), "sub.sock")
	pubSock := filepath.Join(t.TempDir(), "pub.sock")

	sub := Dial("unix://"+dirSock, ClientOptions().
		SetName("sub").SetListenAddr("unix://"+subSock).
		SetHeartInterval(time.Second).SetCheckInterval(100*time.Millisecond))
	defer sub.Close()
	pub := Dial("unix://"+dirSock, ClientOptions().
		SetName("pub").SetListenAddr("unix://"+pubSock).
		SetHeartInterval(time.Second).SetCheckInterval(100*time.Millisecond))
	defer pub.Close()

	got := make(chan string, 4)
	if err := sub.On("uds.test", func(s string) error { got <- s; return nil }); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 15*time.Second, "UDS 订阅同步 + 送达", func() bool {
		if peerCount(pub) != 1 || subCount(pub, "uds.test") != 1 {
			return false
		}
		if err := pub.Emit("uds.test", "hello"); err != nil {
			return false
		}
		select {
		case v := <-got:
			return v == "hello"
		default:
			return false
		}
	})
}

// 服务端重启：节点应自动重连服务端，重新拿到列表并恢复路由。
func TestServerRestart(t *testing.T) {
	sock := fmt.Sprintf("/tmp/event-dir-%d.sock", time.Now().UnixNano())
	t.Cleanup(func() { os.Remove(sock) })
	addr := "unix://" + sock

	dir := NewServer(ServerOptions().SetHeartInterval(time.Second))
	go dir.Listen(addr)

	sub := Dial(addr, ClientOptions().
		SetName("sub").SetListenAddr("unix:///tmp/event-dirrestart-sub.sock").
		SetHeartInterval(time.Second).SetCheckInterval(100*time.Millisecond))
	defer sub.Close()
	pub := Dial(addr, ClientOptions().
		SetName("pub").SetListenAddr("unix:///tmp/event-dirrestart-pub.sock").
		SetHeartInterval(time.Second).SetCheckInterval(100*time.Millisecond))
	defer pub.Close()

	got := make(chan string, 16)
	if err := sub.On("dir.restart", func(s string) error { got <- s; return nil }); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, "首轮订阅同步 + 送达", func() bool {
		if peerCount(pub) != 1 || subCount(pub, "dir.restart") != 1 {
			return false
		}
		if err := pub.Emit("dir.restart", "before"); err != nil {
			return false
		}
		select {
		case v := <-got:
			return v == "before"
		default:
			return false
		}
	})

	_ = dir.Shutdown()
	dir2 := NewServer(ServerOptions().SetHeartInterval(time.Second))
	go dir2.Listen(addr)
	t.Cleanup(func() { _ = dir2.Shutdown() })

	waitFor(t, 15*time.Second, "服务端重启后恢复路由", func() bool {
		if peerCount(pub) != 1 || subCount(pub, "dir.restart") != 1 {
			return false
		}
		if err := pub.Emit("dir.restart", "after"); err != nil {
			return false
		}
		select {
		case v := <-got:
			return v == "after"
		default:
			return false
		}
	})
}

// 节点下线，服务端通知其他节点，peer 连接被清理。
func TestNodeDownNotify(t *testing.T) {
	addr := startServer(t)
	serverAddr := addr
	_ = serverAddr
	sub := startNode(t, addr, "sub", ClientOptions().SetHeartInterval(time.Second))
	pub := startNode(t, addr, "pub", ClientOptions().SetHeartInterval(time.Second))

	if err := sub.On("itest.down", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	waitRoute(t, sub, pub, "itest.down")

	// sub 节点整个下线
	sub.Close()

	waitFor(t, 10*time.Second, "pub 清理 peer", func() bool {
		return peerCount(pub) == 0
	})
}

// 同一节点 On 之后立刻 Emit，事件不丢（本地订阅者直接执行，不经过网络）。
func TestOnThenEmitIsNotLost(t *testing.T) {
	addr := startServer(t)
	c := startNode(t, addr, "self", ClientOptions().SetHeartInterval(time.Second).SetCheckInterval(200*time.Millisecond))

	for i := 0; i < 20; i++ {
		en := eventname.T(fmt.Sprintf("itest.order-%d", i))
		got := make(chan string, 4)
		if err := c.On(en, func(s string) error {
			got <- s
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := c.Emit(en, "x"); err != nil {
			t.Fatalf("第 %d 轮 Emit 报错: %v", i+1, err)
		}
		select {
		case v := <-got:
			if v != "x" {
				t.Fatalf("第 %d 轮参数不对: %q", i+1, v)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("第 %d 轮事件丢失", i+1)
		}
	}
}

// 节点监听多个地址（TCP 随机端口 + UDS），其他节点通过任一地址连接。
func TestMultiListenAddr(t *testing.T) {
	addr := startServer(t)

	udsSock := filepath.Join(t.TempDir(), "multi.sock")
	sub := Dial(addr, ClientOptions().
		SetName("multi-sub").
		SetListenAddr("127.0.0.1:0", "unix://"+udsSock).
		SetHeartInterval(time.Second).SetCheckInterval(100*time.Millisecond))
	defer sub.Close()
	pub := startNode(t, addr, "multi-pub", ClientOptions().SetHeartInterval(time.Second))

	got := make(chan string, 4)
	if err := sub.On("multi.addr", func(s string) error { got <- s; return nil }); err != nil {
		t.Fatal(err)
	}
	waitRoute(t, sub, pub, "multi.addr")

	if err := pub.Emit("multi.addr", "hello"); err != nil {
		t.Fatalf("Emit 报错: %v", err)
	}
	select {
	case v := <-got:
		if v != "hello" {
			t.Fatalf("收到意外内容: %q", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("多地址监听下事件没送达")
	}
}

// 不 SetListenAddr 时，默认监听 127.0.0.1:0（随机端口）。
func TestDefaultListenAddr(t *testing.T) {
	addr := startServer(t)

	sub := Dial(addr, ClientOptions().
		SetName("def-sub").
		SetHeartInterval(time.Second).SetCheckInterval(100*time.Millisecond))
	defer sub.Close()
	pub := startNode(t, addr, "def-pub", ClientOptions().SetHeartInterval(time.Second))

	got := make(chan string, 4)
	if err := sub.On("def.addr", func(s string) error { got <- s; return nil }); err != nil {
		t.Fatal(err)
	}
	waitRoute(t, sub, pub, "def.addr")

	if err := pub.Emit("def.addr", "hello"); err != nil {
		t.Fatalf("Emit 报错: %v", err)
	}
	select {
	case v := <-got:
		if v != "hello" {
			t.Fatalf("收到意外内容: %q", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("默认监听地址下事件没送达")
	}
}

// Builder 链式构建一次发送：自定义消息类型、事件名、发送选项、参数。
func TestBuilder(t *testing.T) {
	addr := startServer(t)
	sub := startNode(t, addr, "builder-sub", ClientOptions().SetHeartInterval(time.Second))
	pub := startNode(t, addr, "builder-pub", ClientOptions().SetHeartInterval(time.Second))

	got := make(chan int, 4)
	if err := sub.On("builder.test", func(n int) error { got <- n; return nil }); err != nil {
		t.Fatal(err)
	}
	waitRoute(t, sub, pub, "builder.test")

	call := pub.Builder().
		BuildMsgType(msgtype.Req).
		BuildEventName("builder.test").
		BuildSendOption(SendOptions().SetTimeout(time.Second)).
		BuildArgs(42).
		Send()

	select {
	case c := <-call.Done:
		if c.Err != nil {
			t.Fatalf("Builder Send 报错: %v", c.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Builder Send 没有完成")
	}
	select {
	case v := <-got:
		if v != 42 {
			t.Fatalf("参数不对: %d", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("回调没触发")
	}
}
