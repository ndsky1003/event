package event

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// measureQPS 建立 listeners 个监听者 + emitters 个发送者,并发压测 duration 时长,
// 返回成功 QPS 与失败次数。
func measureQPS(t *testing.T, url string, listeners, emitters int, duration time.Duration) (qps float64, fails int64) {
	t.Helper()

	for i := 0; i < listeners; i++ {
		l := Dial(url)
		waitConnected(t, l)
		if err := l.On("qps_ev", func(v int) error { return nil }); err != nil {
			t.Fatalf("On: %v", err)
		}
	}

	es := make([]*Client, emitters)
	for i := 0; i < emitters; i++ {
		e := Dial(url)
		waitConnected(t, e)
		es[i] = e
	}

	var total, fail atomic.Int64
	var wg sync.WaitGroup
	stop := time.Now().Add(duration)
	for i := 0; i < emitters; i++ {
		wg.Add(1)
		go func(e *Client) {
			defer wg.Done()
			for time.Now().Before(stop) {
				if err := e.Emit("qps_ev", 1); err != nil {
					fail.Add(1)
				} else {
					total.Add(1)
				}
			}
		}(es[i])
	}
	wg.Wait()

	return float64(total.Load()) / duration.Seconds(), fail.Load()
}

func TestQPS(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过耗时 QPS 压力测试,用 go test -run TestQPS -v 显式运行")
	}
	url := startServer(t)

	scenarios := []struct {
		name      string
		listeners int
		emitters  int
	}{
		{"1监听者×1发送者(串行)", 1, 1},
		{"1监听者×10发送者(并发)", 1, 10},
		{"1监听者×50发送者(并发)", 1, 50},
		{"10监听者×1发送者(广播串行)", 10, 1},
		{"10监听者×10发送者(广播并发)", 10, 10},
	}

	const dur = 3 * time.Second
	fmt.Println("\n===== QPS 压力测试结果 =====")
	fmt.Printf("%-28s %12s %10s\n", "场景", "QPS(次/秒)", "失败数")
	for _, sc := range scenarios {
		qps, fails := measureQPS(t, url, sc.listeners, sc.emitters, dur)
		fmt.Printf("%-28s %12.0f %10d\n", sc.name, qps, fails)
	}
	fmt.Println("============================")
}
