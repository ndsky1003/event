package event

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ndsky1003/event/options"
)

func startServerHandle(t *testing.T) (*server, string) {
	t.Helper()
	url := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	s := NewServer()
	go s.Listen(url)
	return s, url
}

func serverStats(s *server) (services, monitorTopics, monitorSids, reqMetas int) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	services = len(s.services)
	reqMetas = len(s.reqMetas)
	monitorTopics = len(s.monitor)
	for _, m := range s.monitor {
		monitorSids += len(m)
	}
	return
}

// 问题1 回归:SetReqTimeout 单位正确,自定义超时生效。
func TestFixReqTimeoutUnit(t *testing.T) {
	s := NewServer(options.Server().SetReqTimeout(500 * time.Millisecond))
	url := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	go s.Listen(url)

	// 监听者:回调阻塞 5 秒(超过 500ms 超时)
	listener := Dial(url)
	waitConnected(t, listener)
	if err := listener.On("slow_ev", func() error {
		time.Sleep(5 * time.Second)
		return nil
	}); err != nil {
		t.Fatalf("On: %v", err)
	}

	emitter := Dial(url)
	waitConnected(t, emitter)

	start := time.Now()
	err := emitter.Emit("slow_ev")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("期望超时错误,实际 nil")
	}
	if !strings.Contains(err.Error(), "req timeout") {
		t.Errorf("期望超时错误,得到 %v", err)
	}
	if elapsed > 4*time.Second {
		t.Errorf("超时未生效:elapsed=%v,应 <4s(修复前为 31.7 年或 5s)", elapsed)
	}
}

// 问题2 回归:无监听者事件的 reqMeta 立即清理,不残留。
func TestFixNoListenerReqMetaCleanup(t *testing.T) {
	s := NewServer()
	url := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	go s.Listen(url)

	emitter := Dial(url)
	waitConnected(t, emitter)

	for i := 0; i < 100; i++ {
		if err := emitter.Emit("no_listener_ev"); err != nil {
			t.Fatalf("Emit: %v", err)
		}
	}

	time.Sleep(200 * time.Millisecond)
	_, _, _, metas := serverStats(s)
	if metas != 0 {
		t.Errorf("无监听者事件后 reqMetas 应清 0,实际 %d", metas)
	}
}

// 问题3 回归:Client.Close 后停止重连并释放 goroutine。
func TestFixClientClose(t *testing.T) {
	s, url := startServerHandle(t)

	base := runtime.NumGoroutine()

	const n = 20
	for i := 0; i < n; i++ {
		c := Dial(url)
		waitConnected(t, c)
		if err := c.On("ev", func() error { return nil }); err != nil {
			t.Fatalf("On: %v", err)
		}
		c.Close()
	}

	// goroutine 应回落到接近 base(允许 server 常驻 goroutine 波动)
	deadline := time.Now().Add(5 * time.Second)
	for {
		now := runtime.NumGoroutine()
		if now <= base+5 {
			t.Logf("goroutine 回落:base=%d, now=%d", base, now)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Close 后 goroutine 泄漏:base=%d, now=%d", base, now)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// server 端 service 应清理(Close 后不再重连)
	deadline = time.Now().Add(5 * time.Second)
	for {
		services, _, sids, _ := serverStats(s)
		if services == 0 && sids == 0 {
			t.Logf("server 清理完成:services=0, monitorSids=0")
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server 未清理:services=%d, monitorSids=%d", services, sids)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Close 幂等:二次调用不 panic。
func TestFixClientCloseIdempotent(t *testing.T) {
	c := Dial("127.0.0.1:1")
	c.Close()
	c.Close() // 不 panic
}
