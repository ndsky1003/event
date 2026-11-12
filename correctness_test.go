package event

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ndsky1003/event/codec"
	"github.com/ndsky1003/event/msg"
)

// mustNotPanic 断言 fn 不 panic,若 panic 则记录失败并暴露 bug,避免拖垮整个测试进程。
func mustNotPanic(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("[%s] 不应 panic,但发生了 panic: %v", name, r)
		}
	}()
	fn()
}

// waitReconnected 等待 client 断开(connecting=false)后重新连上(connecting=true)。
func waitReconnected(t *testing.T, c *Client) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !c.getConnecting() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	for time.Now().Before(deadline) {
		if c.getConnecting() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("client 未在时限内重连")
}

// ---- 已知 bug 验证(预期正确行为,当前实现会 panic) ----

func TestOnInvalidRegexpShouldNotPanic(t *testing.T) {
	url := startServer(t)
	c := Dial(url)
	mustNotPanic(t, "非法正则 On", func() {
		err := c.On("/[/", func(s []string) error { return nil })
		if err == nil {
			t.Errorf("非法正则应返回错误,实际返回 nil")
		}
	})
}

func TestOnEmptyRegexpShouldNotPanic(t *testing.T) {
	url := startServer(t)
	c := Dial(url)
	mustNotPanic(t, "空正则 On", func() {
		err := c.On("/", func(s []string) error { return nil })
		if err == nil {
			t.Errorf("空正则应返回错误,实际返回 nil")
		}
	})
}

// ---- 功能正确性 ----

// handler panic 应被捕获转为错误,不拖垮进程。
func TestHandlerPanicRecovered(t *testing.T) {
	url := startServer(t)

	listener := Dial(url)
	if err := listener.On("panic_ev", func() error {
		panic("handler boom")
	}); err != nil {
		t.Fatalf("On: %v", err)
	}

	emitter := Dial(url)
	waitConnected(t, emitter)
	err := emitter.Emit("panic_ev")
	if err == nil {
		t.Fatal("期望 panic 被转为错误返回,实际 nil")
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Fatalf("错误 %q 应包含 panic 信息", err.Error())
	}
}

// 正则多个捕获组,submatch 应完整传递。
func TestRegexMultipleGroups(t *testing.T) {
	url := startServer(t)

	received := make(chan string, 1)
	listener := Dial(url)
	if err := listener.On("/user_([0-9]+)_([a-z]+)/", func(sub []string, n int) error {
		received <- fmt.Sprintf("%v|%d", sub, n)
		return nil
	}); err != nil {
		t.Fatalf("On: %v", err)
	}

	emitter := Dial(url)
	waitConnected(t, emitter)
	if err := emitter.Emit("user_42_alice", 7); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	got := waitEvent(t, received)
	if !strings.HasPrefix(got, "[user_42_alice 42 alice]|7") {
		t.Fatalf("submatch 传递错误:%q", got)
	}
}

// 同一 client 同时注册精确与正则监听,事件命中两者时应去重广播(只触发一次 handler 运行,
// 且两条监听回调都会被执行)。这里用两个独立 client 分别注册,验证服务端广播去重:
// 一个 client 注册两个匹配 topic,应只收到一次。
func TestBroadcastDedupSameClient(t *testing.T) {
	url := startServer(t)

	var mu sync.Mutex
	var hits int
	listener := Dial(url)
	// 精确监听
	if err := listener.On("dedup_ev", func() error {
		mu.Lock()
		hits++
		mu.Unlock()
		return nil
	}); err != nil {
		t.Fatalf("On exact: %v", err)
	}
	// 正则监听同一事件
	if err := listener.On("/dedup_[a-z]+/", func([]string) error {
		mu.Lock()
		hits++
		mu.Unlock()
		return nil
	}); err != nil {
		t.Fatalf("On regexp: %v", err)
	}

	emitter := Dial(url)
	waitConnected(t, emitter)
	if err := emitter.Emit("dedup_ev"); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	// 两条监听都应触发,但服务端只广播一次;客户端本地两条 handler 都会执行 => 2 次。
	if hits != 2 {
		t.Errorf("命中数应为 2(两条 handler),实际 %d", hits)
	}
}

// 多个监听者都返回错误时,错误应被聚合。
func TestMultipleHandlersErrorAggregation(t *testing.T) {
	url := startServer(t)

	for i := 0; i < 3; i++ {
		i := i
		l := Dial(url)
		if err := l.On("agg_ev", func() error {
			return fmt.Errorf("err_%d", i)
		}); err != nil {
			t.Fatalf("On: %v", err)
		}
	}

	emitter := Dial(url)
	waitConnected(t, emitter)
	err := emitter.Emit("agg_ev")
	if err == nil {
		t.Fatal("期望聚合错误,实际 nil")
	}
	for i := 0; i < 3; i++ {
		if !strings.Contains(err.Error(), fmt.Sprintf("err_%d", i)) {
			t.Errorf("聚合错误缺少 err_%d: %v", i, err)
		}
	}
}

// 部分监听者失败,整体应报错。
func TestPartialHandlerError(t *testing.T) {
	url := startServer(t)

	ok := Dial(url)
	if err := ok.On("partial_ev", func() error { return nil }); err != nil {
		t.Fatalf("On ok: %v", err)
	}
	bad := Dial(url)
	if err := bad.On("partial_ev", func() error { return errors.New("bad one") }); err != nil {
		t.Fatalf("On bad: %v", err)
	}

	emitter := Dial(url)
	waitConnected(t, emitter)
	if err := emitter.Emit("partial_ev"); err == nil {
		t.Fatal("部分监听者失败应返回错误,实际 nil")
	}
}

// 断开后重连,已注册事件应重新生效。
func TestReconnectRestoresEvents(t *testing.T) {
	url := startServer(t)

	received := make(chan string, 1)
	listener := Dial(url)
	if err := listener.On("re_ev", func(v int) error {
		received <- fmt.Sprintf("%d", v)
		return nil
	}); err != nil {
		t.Fatalf("On: %v", err)
	}

	emitter := Dial(url)
	waitConnected(t, emitter)

	// 首次验证
	if err := emitter.Emit("re_ev", 1); err != nil {
		t.Fatalf("Emit 1: %v", err)
	}
	if got := waitEvent(t, received); got != "1" {
		t.Fatalf("首次收到 %q", got)
	}

	// 强制断开 listener
	if listener.codec != nil {
		listener.codec.Close()
	}
	waitReconnected(t, listener)

	// 重连后再次 Emit,应能收到
	if err := emitter.Emit("re_ev", 2); err != nil {
		t.Fatalf("Emit 2: %v", err)
	}
	if got := waitEvent(t, received); got != "2" {
		t.Fatalf("重连后收到 %q,期望 2", got)
	}
}

// 参数个数不匹配不应 panic:发送参数少于/多于 handler 声明。
func TestArgCountMismatch(t *testing.T) {
	url := startServer(t)

	// handler 声明 3 个参数
	received := make(chan string, 1)
	listener := Dial(url)
	if err := listener.On("mismatch", func(a, b, c int) error {
		received <- fmt.Sprintf("%d,%d,%d", a, b, c)
		return nil
	}); err != nil {
		t.Fatalf("On: %v", err)
	}

	emitter := Dial(url)
	waitConnected(t, emitter)

	// 只发 1 个参数,其余应填零值
	if err := emitter.Emit("mismatch", 9); err != nil {
		t.Fatalf("Emit 少参数: %v", err)
	}
	if got := waitEvent(t, received); got != "9,0,0" {
		t.Fatalf("少参数结果 %q,期望 9,0,0", got)
	}

	// 多参数(4 个),多余应忽略
	received2 := make(chan string, 1)
	l2 := Dial(url)
	if err := l2.On("mismatch2", func(a int) error {
		received2 <- fmt.Sprintf("%d", a)
		return nil
	}); err != nil {
		t.Fatalf("On 2: %v", err)
	}
	if err := emitter.Emit("mismatch2", 1, 2, 3, 4); err != nil {
		t.Fatalf("Emit 多参数: %v", err)
	}
	if got := waitEvent(t, received2); got != "1" {
		t.Fatalf("多参数结果 %q,期望 1", got)
	}
}

// 发送无法 gob 编码的参数(chan),应返回错误而非 panic。
func TestUnencodableArg(t *testing.T) {
	url := startServer(t)

	emitter := Dial(url)
	waitConnected(t, emitter)

	mustNotPanic(t, "无法编码参数 Emit", func() {
		err := emitter.Emit("no_enc", make(chan int))
		if err == nil {
			t.Error("期望编码错误,实际 nil")
		}
	})
}

// 零参数事件。
func TestEmitZeroArgs(t *testing.T) {
	url := startServer(t)

	received := make(chan struct{}, 1)
	listener := Dial(url)
	if err := listener.On("zero", func() error {
		received <- struct{}{}
		return nil
	}); err != nil {
		t.Fatalf("On: %v", err)
	}

	emitter := Dial(url)
	waitConnected(t, emitter)
	if err := emitter.Emit("zero"); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("零参数事件未触发")
	}
}

// 混合参数类型:值类型、指针、字符串、bool。
func TestMixedArgTypes(t *testing.T) {
	url := startServer(t)

	received := make(chan string, 1)
	listener := Dial(url)
	if err := listener.On("mixed", func(i int, s string, b bool, p *testPerson) error {
		received <- fmt.Sprintf("%d|%s|%v|%s:%d", i, s, b, p.Name, p.Age)
		return nil
	}); err != nil {
		t.Fatalf("On: %v", err)
	}

	emitter := Dial(url)
	waitConnected(t, emitter)
	if err := emitter.Emit("mixed", 5, "hi", true, &testPerson{Name: "x", Age: 3}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if got := waitEvent(t, received); got != "5|hi|true|x:3" {
		t.Fatalf("混合参数结果 %q", got)
	}
}

// nil 指针参数:gob 无法编码 nil 指针,库应将 panic 转为错误而非崩溃进程。
func TestNilPointerArg(t *testing.T) {
	url := startServer(t)

	emitter := Dial(url)
	waitConnected(t, emitter)

	mustNotPanic(t, "Emit nil 指针", func() {
		err := emitter.Emit("nil_ptr", (*testPerson)(nil))
		if err == nil {
			t.Error("期望 nil 指针编码错误,实际 nil")
		}
	})
}

// ---- 并发正确性(-race) ----

// 并发 Emit 与 Seq 同时访问 seq 字段。
func TestConcurrentEmitAndSeq(t *testing.T) {
	url := startServer(t)

	listener := Dial(url)
	if err := listener.On("c_seq", func(v int) error { return nil }); err != nil {
		t.Fatalf("On: %v", err)
	}
	emitter := Dial(url)
	waitConnected(t, emitter)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					emitter.Emit("c_seq", 1)
					_ = emitter.Seq()
				}
			}
		}()
	}
	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// 并发 Emit 与 PrintCall 同时访问 pending 字段。
func TestConcurrentEmitAndPrintCall(t *testing.T) {
	url := startServer(t)

	listener := Dial(url)
	if err := listener.On("c_print", func(v int) error { return nil }); err != nil {
		t.Fatalf("On: %v", err)
	}
	emitter := Dial(url)
	waitConnected(t, emitter)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("PrintCall 并发崩溃(panic): %v", r)
				}
			}()
			for {
				select {
				case <-stop:
					return
				default:
					emitter.EmitAsync("c_print", 1)
					emitter.PrintCall()
				}
			}
		}()
	}
	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// service.write 写失败会 close(done);多次写失败会重复 close(done) 导致 panic。
// 直接单测复现:同一 service 连续两次写失败。
func TestServiceWriteCloseTwice(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	s := newService(nil, 1, codec.NewGobCodec(serverConn))
	clientConn.Close() // 关闭对端,使后续写失败

	mustNotPanic(t, "service 二次写失败", func() {
		_ = s.write(&msg.Msg{T: msg.MsgType_res})
		_ = s.write(&msg.Msg{T: msg.MsgType_res})
	})
}

// 并发 Emit 时 Close,验证客户端自身状态清理的正确性(不含 server 端崩溃)。
// 注意:该测试会触发 server 端 service.write 重复 close(done) 导致进程崩溃,
// 该 bug 已由 TestServiceWriteCloseTwice 精确复现,此处跳过以免拖垮测试进程。
func TestConcurrentEmitAndClose(t *testing.T) {
	t.Skip("已知 bug:触发 server 端 close of closed channel 崩溃,见 TestServiceWriteCloseTwice")

	url := startServer(t)

	listener := Dial(url)
	if err := listener.On("c_close", func(v int) error { return nil }); err != nil {
		t.Fatalf("On: %v", err)
	}

	emitters := make([]*Client, 10)
	for i := range emitters {
		e := Dial(url)
		waitConnected(t, e)
		emitters[i] = e
	}

	var wg sync.WaitGroup
	var stopAll atomic.Bool
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(e *Client) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("并发 Emit/Close 客户端崩溃(panic): %v", r)
				}
			}()
			for !stopAll.Load() {
				e.EmitAsync("c_close", 1)
			}
		}(emitters[i])
	}

	time.Sleep(100 * time.Millisecond)
	for _, e := range emitters {
		e.Close()
	}
	stopAll.Store(true)
	wg.Wait()
}

// 高频广播下验证监听者计数准确。
func TestBroadcastExactCount(t *testing.T) {
	url := startServer(t)

	const n = 20
	var mu sync.Mutex
	var hits int64
	for i := 0; i < n; i++ {
		l := Dial(url)
		waitConnected(t, l)
		if err := l.On("count_ev", func() error {
			mu.Lock()
			hits++
			mu.Unlock()
			return nil
		}); err != nil {
			t.Fatalf("On: %v", err)
		}
	}

	emitter := Dial(url)
	waitConnected(t, emitter)
	const emits = 10
	for i := 0; i < emits; i++ {
		if err := emitter.Emit("count_ev"); err != nil {
			t.Fatalf("Emit: %v", err)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		h := hits
		mu.Unlock()
		if h == n*emits {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("广播计数不匹配:got=%d want=%d", h, n*emits)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// 事件类型消息字符串化(含未定义类型)。
func TestServerErrorType(t *testing.T) {
	var e ServerError = "boom"
	if e.Error() != "boom" {
		t.Fatalf("ServerError.Error() = %q", e.Error())
	}
}

// EmitAsync 无监听者时不应报错。
func TestEmitAsyncNoListener(t *testing.T) {
	url := startServer(t)
	emitter := Dial(url)
	waitConnected(t, emitter)

	call := emitter.EmitAsync("no_listener_async", 1)
	if call == nil {
		t.Fatal("EmitAsync 返回 nil")
	}
	select {
	case <-call.Done:
		if call.Error != nil {
			t.Fatalf("无监听者 EmitAsync 不应报错,got %v", call.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EmitAsync 超时")
	}
}

// 事件类型为 msg.EventType 的完整往返。
func TestEventTypeRoundTrip(t *testing.T) {
	url := startServer(t)

	received := make(chan msg.EventType, 1)
	listener := Dial(url)
	if err := listener.On(msg.EventType("rt_ev"), func() error {
		received <- "rt_ev"
		return nil
	}); err != nil {
		t.Fatalf("On: %v", err)
	}

	emitter := Dial(url)
	waitConnected(t, emitter)
	if err := emitter.Emit(msg.EventType("rt_ev")); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	select {
	case got := <-received:
		if got != "rt_ev" {
			t.Fatalf("收到 %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("超时")
	}
}
