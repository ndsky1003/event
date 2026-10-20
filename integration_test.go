package event

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ndsky1003/event/v2/eventname"
	"github.com/ndsky1003/event/v2/msg"
	"github.com/ndsky1003/event/v2/msgtype"
)

// 端到端集成测试：走真实的 TCP + 新帧格式 + coder（默认 msgpack）。
// 注意包里的 TestMain 会先占 127.0.0.1:8080，这里一律用随机端口。

func startServer(t *testing.T, opts ...*ServerOption) (string, *server) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(opts...)
	go srv.Listen(addr)
	// 每个用例都把自己的服务端关掉：否则 accept 循环、timer.Run
	// 和一堆还在重连的客户端会一路攒到整个测试包结束
	t.Cleanup(func() {
		if err := srv.Shutdown(); err != nil {
			t.Logf("Shutdown: %v", err)
		}
	})
	return addr, srv
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

func serviceCount(srv *server) int {
	srv.l.Lock()
	defer srv.l.Unlock()
	return len(srv.services)
}

// listenerCount 必须持锁读：server.on() 在锁内写 exact / regs
func listenerCount(srv *server) int {
	srv.l.Lock()
	defer srv.l.Unlock()
	n := 0
	for _, ts := range srv.exact {
		n += len(ts.sids)
	}
	for _, ts := range srv.regs {
		n += len(ts.sids)
	}
	return n
}

func TestEmitOnRoundTrip(t *testing.T) {
	addr, _ := startServer(t, ServerOptions().SetHeartInterval(time.Second))
	c := Dial(addr, ClientOptions().
		SetName("roundtrip").
		SetHeartInterval(time.Second).
		SetCheckInterval(200*time.Millisecond))
	defer c.Stop(nil)

	got := make(chan string, 8)
	c.On("itest.echo", func(name string) error {
		got <- name
		return nil
	})

	// topic 注册是异步的，前几次 Emit 可能还没有监听者，重试直到真的送达
	var lastErr error
	for i := 0; i < 40; i++ {
		lastErr = c.Emit("itest.echo", "hello")
		select {
		case v := <-got:
			if v != "hello" {
				t.Fatalf("收到意外内容: %q", v)
			}
			t.Logf("第 %d 次 Emit 成功送达", i+1)
			return
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("Emit 始终没有送达，lastErr=%v", lastErr)
}

func TestHandlerErrorPropagatesToEmitter(t *testing.T) {
	addr, _ := startServer(t, ServerOptions().SetHeartInterval(time.Second))
	c := Dial(addr, ClientOptions().
		SetName("errcase").
		SetHeartInterval(time.Second).
		SetCheckInterval(200*time.Millisecond))
	defer c.Stop(nil)

	c.On("itest.fail", func(s string) error {
		return errors.New("handler boom")
	})

	var lastErr error
	for i := 0; i < 40; i++ {
		lastErr = c.Emit("itest.fail", "x")
		if lastErr != nil && strings.Contains(lastErr.Error(), "handler boom") {
			t.Logf("错误已回传: %v", lastErr)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("handler 的错误没有回传，lastErr=%v", lastErr)
}

func TestRegexTopic(t *testing.T) {
	addr, _ := startServer(t, ServerOptions().SetHeartInterval(time.Second))
	c := Dial(addr, ClientOptions().
		SetName("regex").
		SetHeartInterval(time.Second).
		SetCheckInterval(200*time.Millisecond))
	defer c.Stop(nil)

	type hit struct {
		sub []string
		n   int
	}
	got := make(chan hit, 8)
	c.On("/itest_reg_[a-z]+/", func(sub []string, n int) error {
		got <- hit{sub: sub, n: n}
		return nil
	})

	var lastErr error
	for i := 0; i < 40; i++ {
		lastErr = c.Emit("itest_reg_abc", 42)
		select {
		case h := <-got:
			if h.n != 42 {
				t.Fatalf("参数解错: %+v", h)
			}
			if len(h.sub) == 0 || h.sub[0] != "itest_reg_abc" {
				t.Fatalf("正则 submatch 不对: %+v", h.sub)
			}
			return
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("正则 topic 没有命中，lastErr=%v", lastErr)
}

// 空闲连接必须靠传输层心跳活下来，不能被判死
func TestIdleConnSurvivesHeartbeat(t *testing.T) {
	addr, _ := startServer(t,
		ServerOptions().SetHeartInterval(300*time.Millisecond))
	c := Dial(addr, ClientOptions().
		SetName("idle").
		SetHeartInterval(300*time.Millisecond).
		SetCheckInterval(200*time.Millisecond))
	defer c.Stop(nil)

	got := make(chan struct{}, 8)
	c.On("itest.idle", func(s string) error { got <- struct{}{}; return nil })
	waitFor(t, 3*time.Second, "topic 注册完成", func() bool {
		if err := c.Emit("itest.idle", "warmup"); err != nil {
			return false
		}
		select {
		case <-got:
			return true
		default:
			return false
		}
	})

	// 静默 3 秒 = 10 个心跳周期，连接不该断
	time.Sleep(3 * time.Second)
	if !c.isConnected() {
		t.Fatal("空闲期间连接断了")
	}
	if err := c.Emit("itest.idle", "after-idle"); err != nil {
		t.Fatalf("静默后 Emit 失败: %v", err)
	}
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("静默后事件没有送达")
	}
}

// 服务端踢掉连接后，客户端要重连并重新注册 topic
func TestReconnectAndReregister(t *testing.T) {
	addr, srv := startServer(t,
		ServerOptions().SetHeartInterval(time.Second))
	c := Dial(addr, ClientOptions().
		SetName("reconn").
		SetHeartInterval(time.Second).
		SetCheckInterval(100*time.Millisecond))
	defer c.Stop(nil)

	got := make(chan string, 16)
	c.On("itest.reconn", func(s string) error { got <- s; return nil })

	waitFor(t, 3*time.Second, "首条连接建立", func() bool { return serviceCount(srv) == 1 })
	waitFor(t, 3*time.Second, "topic 注册成功", func() bool {
		return c.Emit("itest.reconn", "before") == nil
	})

	// 服务端主动掐掉这条连接
	srv.l.Lock()
	var sid uint32
	for id := range srv.services {
		sid = id
	}
	srv.l.Unlock()
	if err := srv.Close(sid, true); err != nil {
		t.Fatalf("服务端关闭连接失败: %v", err)
	}

	waitFor(t, 5*time.Second, "客户端重连并重新注册", func() bool {
		if serviceCount(srv) != 1 || !c.isConnected() {
			return false
		}
		return c.Emit("itest.reconn", "after") == nil
	})
	select {
	case v := <-got:
		t.Logf("重连后收到（可能是重连前的残留）: %q", v)
	case <-time.After(3 * time.Second):
		t.Fatal("重连后事件没有送达")
	}
}

func TestWrongSecretRejected(t *testing.T) {
	addr, srv := startServer(t,
		ServerOptions().SetSecret("right").SetHeartInterval(time.Second))
	c := Dial(addr, ClientOptions().
		SetName("bad").SetSecret("wrong").
		SetHeartInterval(time.Second).
		SetCheckInterval(100*time.Millisecond))
	defer c.Stop(nil)

	time.Sleep(600 * time.Millisecond)
	if n := serviceCount(srv); n != 0 {
		t.Fatalf("错误 secret 的连接不该注册成功，当前 services=%d", n)
	}
	err := c.Emit("itest.any", "x")
	if err == nil {
		t.Fatal("错误 secret 下 Emit 不该成功")
	}
	t.Logf("错误 secret 被拒: %v", err)
}

// 多个监听者的错误要聚合回发布者
func TestMultipleListenersErrorsAggregated(t *testing.T) {
	addr, _ := startServer(t, ServerOptions().SetHeartInterval(time.Second))
	const n = 3
	c1 := Dial(addr, ClientOptions().SetName("pub").
		SetHeartInterval(time.Second).SetCheckInterval(200*time.Millisecond))
	defer c1.Stop(nil)

	for i := 0; i < n; i++ {
		// go.mod 是 go 1.20，循环变量是共享的，必须自己捕获
		idx := i
		c := Dial(addr, ClientOptions().SetName(fmt.Sprintf("sub%d", idx)).
			SetHeartInterval(time.Second).SetCheckInterval(200*time.Millisecond))
		defer c.Stop(nil)
		c.On("itest.multi", func(s string) error {
			return fmt.Errorf("boom-%d", idx)
		})
	}

	var lastErr error
	for i := 0; i < 60; i++ {
		lastErr = c1.Emit("itest.multi", "x")
		if lastErr != nil {
			all := true
			for j := 0; j < n; j++ {
				if !strings.Contains(lastErr.Error(), fmt.Sprintf("boom-%d", j)) {
					all = false
					break
				}
			}
			if all {
				t.Logf("聚合到全部 %d 个错误: %v", n, lastErr)
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("没有聚合到全部错误，lastErr=%v", lastErr)
}

// 投递失败必须马上释放 pending，不能让发布者干等 Timeout。
// 直接往 monitor 里塞一个 services 里不存在的 sid，模拟"快照里有、投递必失败"的窗口，
// 这样测试是确定的，不依赖 EOF 与 Emit 谁先到。
func TestDispatchFailureReleasesPending(t *testing.T) {
	const timeout = 5 * time.Second
	addr, srv := startServer(t, ServerOptions().SetTimeout(timeout).SetHeartInterval(time.Second))

	sub := Dial(addr, ClientOptions().SetName("sub").SetHeartInterval(time.Second).SetCheckInterval(200*time.Millisecond))
	defer sub.Stop(nil)
	sub.On("itest.dead", func() error { return nil })

	pub := Dial(addr, ClientOptions().SetName("pub").SetCheckInterval(200*time.Millisecond))
	defer pub.Stop(nil)

	waitFor(t, 5*time.Second, "监听者注册", func() bool { return listenerCount(srv) > 0 })

	srv.l.Lock()
	var ts *topicSids
	for _, ts = range srv.exact {
		break
	}
	if ts != nil {
		ts.sids[999999] = struct{}{} //幽灵监听者：在索引里，但不在 services 里
	}
	srv.l.Unlock()
	if ts == nil {
		t.Fatal("没找到已注册的 topic 条目")
	}

	start := time.Now()
	err := pub.Emit("itest.dead")
	cost := time.Since(start)

	if cost > timeout/2 {
		t.Fatalf("Emit 耗时 %v，逼近 Timeout(%v)：pending 没被投递失败释放", cost.Round(time.Millisecond), timeout)
	}
	if err == nil {
		t.Fatal("投递失败应该把错误回给发布者，结果 err=nil")
	}
	t.Logf("投递失败快速返回：耗时=%v err=%v", cost.Round(time.Millisecond), err)
}

// On 之后立刻 Emit，事件必须送达。
// 修复前 On 用 go regist_topic()，注册帧和紧随其后的 Emit 是两条并发消息，
// Emit 可能先到服务端 —— 那时 topic 还没注册，这次 Emit 变成"无人监听"，
// 不报错也不送达，事件静默丢失。
func TestOnThenEmitIsNotLost(t *testing.T) {
	addr, _ := startServer(t, ServerOptions().SetHeartInterval(time.Second))

	pub := Dial(addr, ClientOptions().SetName("pub-order").SetCheckInterval(200*time.Millisecond))
	defer pub.Stop(nil)
	waitFor(t, 5*time.Second, "发布者连接", func() bool { return pub.isConnected() })

	for i := 0; i < 20; i++ {
		en := eventname.T(fmt.Sprintf("itest.order-%d", i))
		got := make(chan string, 4)
		sub := Dial(addr, ClientOptions().
			SetName(fmt.Sprintf("sub-order-%d", i)).
			SetHeartInterval(time.Second).
			SetCheckInterval(10*time.Second))
		// Dial 是异步的，先等连接起来；否则 On 只会把 topic 记在本地，
		// 要等 connect 成功才上报，测的就不是顺序问题了
		waitFor(t, 5*time.Second, "监听者连接", func() bool { return sub.isConnected() })
		sub.On(en, func(s string) error {
			got <- s
			return nil
		})
		// 关键：On 之后不做任何等待，直接 Emit
		if err := pub.Emit(en, "x"); err != nil {
			t.Fatalf("第 %d 轮 Emit 报错: %v", i+1, err)
		}
		select {
		case v := <-got:
			if v != "x" {
				t.Fatalf("第 %d 轮参数不对: %q", i+1, v)
			}
		case <-time.After(10 * time.Second):
			sub.Close()
			t.Fatalf("第 %d 轮事件丢失：On 之后立刻 Emit 没有送达", i+1)
		}
		sub.Close()
	}
}

// 监听者已经收下请求、还没回包就断线：服务端必须立刻结算，
// 不能让发布者干等 Timeout（默认 10s）。
func TestListenerDeathReleasesPending(t *testing.T) {
	// Timeout 取默认量级，收尾窗口取它的 40%：
	// 有修复时是毫秒级，没修复时要等满 Timeout，两者差一个数量级，不会误判。
	const timeout = 10 * time.Second
	addr, srv := startServer(t, ServerOptions().SetTimeout(timeout).SetHeartInterval(time.Second))

	started := make(chan struct{})
	sub := Dial(addr, ClientOptions().
		SetName("slow-sub").
		SetHeartInterval(time.Second).
		SetCheckInterval(10*time.Second))
	sub.On("itest.slow", func() error {
		close(started)
		time.Sleep(30 * time.Second) // 永远不回包
		return nil
	})

	pub := Dial(addr, ClientOptions().SetName("pub").SetCheckInterval(200*time.Millisecond))
	defer pub.Stop(nil)

	waitFor(t, 5*time.Second, "发布者连接", func() bool { return pub.isConnected() })
	waitFor(t, 10*time.Second, "监听者注册", func() bool { return listenerCount(srv) > 0 })

	call := pub.EmitAsync("itest.slow")
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("监听者没有收到请求")
	}

	// 监听者带着这个未完成的请求断线
	sub.Stop(nil)

	select {
	case c := <-call.Done:
		if c.Err == nil {
			t.Fatal("监听者断线后应该回一个错误，结果 err=nil")
		}
		if !strings.Contains(c.Err.Error(), "connection lost") {
			t.Logf("错误内容: %v", c.Err)
		}
		srv.l.Lock()
		pending := len(srv.pending)
		srv.l.Unlock()
		if pending != 0 {
			t.Fatalf("pending 没清干净，还剩 %d 条", pending)
		}
		t.Logf("监听者断线立即结算: %v", c.Err)
	case <-time.After(timeout * 2 / 5):
		t.Fatalf("等了 %v 还没结果：监听者欠的响应没有被断线收尾", timeout*2/5)
	}
}

// Close 之后不能偷偷重连。
// Dial 出来的 keepAlive goroutine 原本永远退不掉，Client 用完只能泄漏。
func TestClientCloseStopsReconnect(t *testing.T) {
	addr, srv := startServer(t, ServerOptions().SetHeartInterval(time.Second))
	c := Dial(addr, ClientOptions().
		SetName("closer").
		SetHeartInterval(time.Second).
		SetCheckInterval(200*time.Millisecond))

	waitFor(t, 10*time.Second, "连接建立", func() bool { return serviceCount(srv) == 1 })

	if err := c.Close(); err != nil {
		t.Logf("Close 返回: %v", err)
	}
	waitFor(t, 10*time.Second, "服务端连接清零", func() bool { return serviceCount(srv) == 0 })

	// 再跨几个重连周期，确认不会偷偷回来
	time.Sleep(time.Second)
	if n := serviceCount(srv); n != 0 {
		t.Fatalf("Close 之后又重连了，服务端连接数=%d", n)
	}
	if c.isConnected() {
		t.Fatal("Close 之后 isConnected 仍为 true")
	}
	// 幂等
	if err := c.Close(); err != nil {
		t.Fatalf("第二次 Close 应返回 nil，实际 %v", err)
	}
	// Close 之后 Emit 必须明确报错，而不是静默成功
	if err := c.Emit("itest.after-close"); err == nil {
		t.Fatal("Close 之后 Emit 应该报错")
	}
}

// 同一个监听者被结算两次（比如先收到 Res，之后连接又断了触发断线收尾），
// 计数不能被减两次：uint64 下溢成天文数字后，pending 再也等不到 0，
// 只能靠 Timeout 收尾，发布者白等。
func TestSettleIsIdempotent(t *testing.T) {
	addr, srv := startServer(t, ServerOptions().SetTimeout(2*time.Second).SetHeartInterval(time.Second))
	sub := Dial(addr, ClientOptions().SetName("dup-sub").SetHeartInterval(time.Second))
	defer sub.Close()
	sub.On("itest.dup", func() error { return nil })
	waitFor(t, 10*time.Second, "监听者注册", func() bool { return listenerCount(srv) > 0 })

	srv.l.Lock()
	var subSID uint32
	for sid, s := range srv.services {
		if s.name == "dup-sub" {
			subSID = sid
		}
	}
	sc := &server_call{
		origin_sid:     subSID,
		origin_seq:     77,
		serverReqCount: 2, // 两个监听者：dup-sub 和一个幽灵
		settled:        map[uint32]struct{}{},
		targets:        []uint32{subSID, 999998},
	}
	srv.pending[999] = sc
	srv.l.Unlock()

	// 同一个 sid 结算三次：真实场景是"先收到它的 Res，之后它连接断了又触发断线收尾"
	for i := 0; i < 3; i++ {
		srv.settle(subSID, "dup-sub", &msg.Msg{T: msgtype.Res, Seq: 999})
	}

	srv.l.Lock()
	left := sc.serverReqCount
	_, stillPending := srv.pending[999]
	srv.l.Unlock()

	// 只有一个监听者真正结算了，计数必须还剩 1，pending 必须还在
	if left != 1 {
		t.Fatalf("计数被多减了：期望剩 1，实际剩 %d（下溢会是个巨大的数）", left)
	}
	if !stillPending {
		t.Fatal("还有一个监听者没结算，pending 不该被提前删掉")
	}
}

// 大量在途请求 + 监听者断线：断线是罕见路径，收尾时扫全部 pending。
// 这个测试保证它不会退化成 quadratic，也不会让发布者等满 Timeout。
func TestListenerDeathReleasesManyPending(t *testing.T) {
	const n = 500
	const timeout = 10 * time.Second
	addr, srv := startServer(t, ServerOptions().SetTimeout(timeout).SetHeartInterval(time.Second))

	blocked := make(chan struct{}, n)
	sub := Dial(addr, ClientOptions().
		SetName("slow-many").
		SetHeartInterval(time.Second).
		SetCheckInterval(10*time.Second))
	sub.On("itest.manyslow", func(i int) error {
		blocked <- struct{}{}
		time.Sleep(30 * time.Second)
		return nil
	})

	pub := Dial(addr, ClientOptions().SetName("pub-many").SetCheckInterval(200*time.Millisecond))
	defer pub.Close()
	waitFor(t, 10*time.Second, "发布者连接", func() bool { return pub.isConnected() })
	waitFor(t, 10*time.Second, "监听者注册", func() bool { return listenerCount(srv) > 0 })

	calls := make([]*Call, 0, n)
	for i := 0; i < n; i++ {
		calls = append(calls, pub.EmitAsync("itest.manyslow", i))
	}
	// 确认请求确实都到了监听者手里（服务端也已经全部登记进 pending）
	for i := 0; i < n; i++ {
		select {
		case <-blocked:
		case <-time.After(10 * time.Second):
			t.Fatalf("只有 %d/%d 条请求到达监听者", i, n)
		}
	}
	waitFor(t, 10*time.Second, "pending 全部登记", func() bool {
		srv.l.Lock()
		defer srv.l.Unlock()
		return len(srv.pending) >= n
	})

	start := time.Now()
	sub.Close() // 带着 n 条未完成的请求断线

	var done, failed int
	for _, c := range calls {
		select {
		case cc := <-c.Done:
			done++
			if cc.Err != nil {
				failed++
			}
		case <-time.After(timeout * 2 / 5):
			t.Fatalf("只结算了 %d/%d 条，耗时 %v", done, n, time.Since(start).Round(time.Millisecond))
		}
	}
	cost := time.Since(start)
	if failed != n {
		t.Fatalf("期望 %d 条全部报错，实际失败 %d 条", n, failed)
	}
	srv.l.Lock()
	left := len(srv.pending)
	srv.l.Unlock()
	if left != 0 {
		t.Fatalf("pending 没清干净，还剩 %d 条", left)
	}
	t.Logf("%d 条在途请求随监听者断线全部结算，耗时 %v", n, cost.Round(time.Millisecond))
}

// Shutdown 必须把服务端彻底收干净：accept 循环退出、连接断开、pending 清零、
// 端口释放、幂等。原来 listen() 里 Accept 出错是裸 continue，
// 没有关 listener 的地方，所以那个循环永远退不出去。
func TestServerShutdown(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(ServerOptions().SetHeartInterval(time.Second))
	listenDone := make(chan struct{})
	go func() {
		srv.Listen(addr)
		close(listenDone)
	}()

	waitFor(t, 10*time.Second, "端口可用", func() bool {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			return false
		}
		c.Close()
		return true
	})

	sub := Dial(addr, ClientOptions().
		SetName("sub-shutdown").
		SetHeartInterval(time.Second).
		SetCheckInterval(10*time.Second))
	defer sub.Close()
	sub.On("itest.shutdown", func() error { return nil })

	pub := Dial(addr, ClientOptions().
		SetName("pub-shutdown").
		SetCheckInterval(10*time.Second))
	defer pub.Close()

	waitFor(t, 10*time.Second, "两条连接就绪", func() bool { return serviceCount(srv) == 2 })
	if err := pub.Emit("itest.shutdown"); err != nil {
		t.Fatalf("Shutdown 前链路不通: %v", err)
	}

	start := time.Now()
	if err := srv.Shutdown(); err != nil {
		t.Fatalf("Shutdown 返回错误: %v", err)
	}
	t.Logf("Shutdown 耗时 %v", time.Since(start).Round(time.Millisecond))

	// 阻塞在 Listen 里的调用必须返回
	select {
	case <-listenDone:
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown 之后 Listen 没有返回")
	}

	srv.l.Lock()
	conns := len(srv.services)
	pending := len(srv.pending)
	listeners := len(srv.listeners)
	srv.l.Unlock()
	if conns != 0 || pending != 0 || listeners != 0 {
		t.Fatalf("没收干净：services=%d pending=%d listeners=%d", conns, pending, listeners)
	}
	if !srv.IsShutdown() {
		t.Fatal("IsShutdown 应为 true")
	}

	// 客户端必须感知到断连，本地挂着的 Call 也会被结算
	waitFor(t, 10*time.Second, "客户端检测到断连",
		func() bool { return !pub.isConnected() && !sub.isConnected() })

	// 端口必须能重新绑定，证明 listener 真的关了
	l2, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("端口没释放: %v", err)
	}
	if err = l2.Close(); err != nil {
		t.Fatal(err)
	}

	// 幂等
	if err = srv.Shutdown(); err != nil {
		t.Fatalf("第二次 Shutdown 应返回 nil，实际 %v", err)
	}

	// 已关闭的服务端：Emit 必须报错，不能静默成功
	if err = pub.Emit("itest.shutdown"); err == nil {
		t.Fatal("服务端已关闭，Emit 应该报错")
	} else {
		t.Logf("关闭后 Emit: %v", err)
	}

	// 已关闭的服务端：Listen 不再 panic，记日志后直接返回
	second := make(chan struct{})
	go func() {
		srv.Listen(addr)
		close(second)
	}()
	select {
	case <-second:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown 之后 Listen 应当立即返回")
	}
}
