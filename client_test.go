package event

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ndsky1003/event/msg"
	"github.com/ndsky1003/event/options"
)

type testPerson struct {
	Name string
	Age  int
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func startServer(t *testing.T, opts ...*options.ServerOptions) string {
	t.Helper()
	url := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	go NewServer(opts...).Listen(url)
	return url
}

func waitConnected(t *testing.T, c *Client) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.getConnecting() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("client not connected in time")
}

func waitEvent(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case got := <-ch:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for event")
		return ""
	}
}

func TestEmitOnBasic(t *testing.T) {
	url := startServer(t)

	received := make(chan string, 1)
	listener := Dial(url)
	if err := listener.On("db_op", func(a int, b string) error {
		received <- fmt.Sprintf("%d:%s", a, b)
		return nil
	}); err != nil {
		t.Fatalf("On: %v", err)
	}

	emitter := Dial(url)
	waitConnected(t, emitter)
	if err := emitter.Emit("db_op", 42, "hello"); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	if got := waitEvent(t, received); got != "42:hello" {
		t.Fatalf("received %q, want %q", got, "42:hello")
	}
}

func TestEmitWithPointerArg(t *testing.T) {
	url := startServer(t)

	received := make(chan string, 1)
	listener := Dial(url)
	if err := listener.On("person", func(p *testPerson) error {
		received <- fmt.Sprintf("%s:%d", p.Name, p.Age)
		return nil
	}); err != nil {
		t.Fatalf("On: %v", err)
	}

	emitter := Dial(url)
	waitConnected(t, emitter)
	if err := emitter.Emit("person", &testPerson{Name: "lpp", Age: 18}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	if got := waitEvent(t, received); got != "lpp:18" {
		t.Fatalf("received %q, want %q", got, "lpp:18")
	}
}

func TestEmitRegex(t *testing.T) {
	url := startServer(t)

	received := make(chan string, 1)
	listener := Dial(url)
	if err := listener.On("/db_([a-z]+)/", func(submatch []string, name string) error {
		received <- fmt.Sprintf("%v:%s", submatch, name)
		return nil
	}); err != nil {
		t.Fatalf("On: %v", err)
	}

	emitter := Dial(url)
	waitConnected(t, emitter)
	if err := emitter.Emit("db_insert", "alice"); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	got := waitEvent(t, received)
	if !strings.HasPrefix(got, "[db_insert insert]:alice") {
		t.Fatalf("received %q", got)
	}
}

func TestEmitMultipleListeners(t *testing.T) {
	url := startServer(t)

	const listenerCount = 3
	received := make(chan string, listenerCount)
	for i := 0; i < listenerCount; i++ {
		c := Dial(url)
		if err := c.On("multi", func() error {
			received <- "hit"
			return nil
		}); err != nil {
			t.Fatalf("On: %v", err)
		}
	}

	emitter := Dial(url)
	waitConnected(t, emitter)
	if err := emitter.Emit("multi"); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	for i := 0; i < listenerCount; i++ {
		if got := waitEvent(t, received); got != "hit" {
			t.Fatalf("listener %d received %q", i, got)
		}
	}
}

func TestEmitNoListener(t *testing.T) {
	url := startServer(t)

	emitter := Dial(url)
	waitConnected(t, emitter)
	if err := emitter.Emit("no_one_listens"); err != nil {
		t.Fatalf("Emit without listener should not error, got %v", err)
	}
}

func TestEmitHandlerError(t *testing.T) {
	url := startServer(t)

	listener := Dial(url)
	if err := listener.On("fails", func() error {
		return errors.New("handler boom")
	}); err != nil {
		t.Fatalf("On: %v", err)
	}

	emitter := Dial(url)
	waitConnected(t, emitter)
	err := emitter.Emit("fails")
	if err == nil {
		t.Fatal("expected error from handler")
	}
	if !strings.Contains(err.Error(), "handler boom") {
		t.Fatalf("error %q does not contain handler error", err.Error())
	}
}

func TestEmitAsync(t *testing.T) {
	url := startServer(t)

	received := make(chan string, 1)
	listener := Dial(url)
	if err := listener.On("async", func(v int) error {
		received <- fmt.Sprintf("%d", v)
		return nil
	}); err != nil {
		t.Fatalf("On: %v", err)
	}

	emitter := Dial(url)
	waitConnected(t, emitter)
	call := emitter.EmitAsync("async", 7)
	if call == nil {
		t.Fatal("EmitAsync returned nil call")
	}
	select {
	case <-call.Done:
		if call.Error != nil {
			t.Fatalf("EmitAsync error: %v", call.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting EmitAsync")
	}

	if got := waitEvent(t, received); got != "7" {
		t.Fatalf("received %q", got)
	}
}

func TestEmitConcurrent(t *testing.T) {
	url := startServer(t)

	var mu sync.Mutex
	var hits int
	listener := Dial(url)
	if err := listener.On("concurrent", func(v int) error {
		mu.Lock()
		hits++
		mu.Unlock()
		return nil
	}); err != nil {
		t.Fatalf("On: %v", err)
	}

	emitter := Dial(url)
	waitConnected(t, emitter)

	const goroutines = 20
	const perGoroutine = 10
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				if err := emitter.Emit("concurrent", j); err != nil {
					t.Errorf("Emit: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if hits != goroutines*perGoroutine {
		t.Errorf("hits = %d, want %d", hits, goroutines*perGoroutine)
	}
}

func TestConcurrentDisconnect(t *testing.T) {
	url := startServer(t)

	const listenerCount = 10
	listeners := make([]*Client, 0, listenerCount)
	for i := 0; i < listenerCount; i++ {
		c := Dial(url)
		if err := c.On(msg.EventType(fmt.Sprintf("ev_%d", i)), func() error { return nil }); err != nil {
			t.Fatalf("On: %v", err)
		}
		listeners = append(listeners, c)
	}

	emitter := Dial(url)
	waitConnected(t, emitter)

	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					emitter.Emit("ev_0")
					emitter.Emit("ev_1")
				}
			}
		}()
	}

	for _, c := range listeners {
		if c.codec != nil {
			c.codec.Close()
		}
	}
	time.Sleep(300 * time.Millisecond)
	close(done)
	wg.Wait()
}

func TestOnInvalidFunc(t *testing.T) {
	url := startServer(t)
	c := Dial(url)

	if err := c.On("", func() error { return nil }); err == nil {
		t.Error("empty event type should error")
	}

	if err := c.On("no_return", func() {}); err == nil {
		t.Error("func without return should error")
	}

	if err := c.On("bad_return", func() int { return 0 }); err == nil {
		t.Error("func returning non-error should error")
	}

	if err := c.On("/reg/", func(int) error { return nil }); err == nil {
		t.Error("regexp func first param must be []string")
	}

	if err := c.On("/reg2/", func() error { return nil }); err == nil {
		t.Error("regexp func must have at least one param")
	}
}

func TestOnNonFuncPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when On called with non-func")
		}
	}()
	Dial("127.0.0.1:1").On("ev", 123)
}
