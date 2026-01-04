package main

import (
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ndsky1003/event/v3"
	"github.com/ndsky1003/event/v3/eventname"
)

var (
	totalRequests atomic.Int64
	totalErrors   atomic.Int64
	totalSuccess  atomic.Int64
)

func main() {
	// 启动 pprof
	go func() {
		log.Println(http.ListenAndServe("localhost:6060", nil))
	}()

	// 启动服务器
	srv := event.NewServer(
		event.ServerOptions().
			SetSecret("test-secret").
			SetTimeout(5 * time.Second).
			SetIsWrapError(false),
	)
	srv.Listen("127.0.0.1:8080")
	log.Println("Server started on :8080")

	// 等待服务器启动
	time.Sleep(500 * time.Millisecond)

	// 启动多个客户端监听器
	numListeners := 5
	for i := 0; i < numListeners; i++ {
		go startListener(i)
	}
	log.Printf("Started %d listeners", numListeners)
	time.Sleep(500 * time.Millisecond)

	// 运行不同模式的压力测试
	fmt.Println("\n==========================================")
	fmt.Println("           压力测试开始")
	fmt.Println("==========================================")

	// 测试 EmitOne (随机发送给一个)
	testEmitOne("EmitOne", 100*time.Millisecond)

	// 测试 EmitAll (发送给所有)
	testEmitAll("EmitAll", 100*time.Millisecond)

	// 测试 EmitFirst (发送给所有，只接受第一个响应)
	testEmitFirst("EmitFirst", 100*time.Millisecond)

	// 长时间压测 - EmitOne
	fmt.Println("\n==========================================")
	fmt.Println("         长时间压测 (10秒)")
	fmt.Println("==========================================")
	runStressTest("EmitOne-Long", 10*time.Second, func(client *event.Client, workers int) {
		stressEmitOne(client, workers)
	})

	fmt.Println("\n==========================================")
	fmt.Println("         最终统计")
	fmt.Println("==========================================")
	fmt.Printf("总请求数: %d\n", totalRequests.Load())
	fmt.Printf("成功数:   %d\n", totalSuccess.Load())
	fmt.Printf("错误数:   %d\n", totalErrors.Load())
}

func startListener(id int) {
	client := event.Dial("127.0.0.1:8080",
		event.ClientOptions().
			SetName(fmt.Sprintf("listener-%d", id)).
			SetSecret("test-secret").
			SetIsWrapError(false),
	)

	// 监听多个事件
	events := []string{
		"event/add",
		"event/query",
		"event/update",
		"event/delete",
	}

	for _, evt := range events {
		// 每个事件注册多个 handler
		for j := 0; j < 3; j++ {
			eventName := eventname.T(evt)
			client.On(eventName, func(params ...any) error {
				// 模拟一些处理
				if len(params) > 0 {
					_ = params[0]
				}
				return nil
			})
		}
	}

	log.Printf("Listener %d registered events", id)
}

func testEmitOne(name string, duration time.Duration) {
	fmt.Printf("\n[%s] 测试 %v\n", name, duration)
	client := event.Dial("127.0.0.1:8080",
		event.ClientOptions().
			SetName("stress-client").
			SetSecret("test-secret").
			SetIsWrapError(false),
	)
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	var count int64
	ticker := time.NewTicker(duration)
	defer ticker.Stop()

	workers := 10
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if err := client.EmitOne("event/add", 123, "test"); err != nil {
					totalErrors.Add(1)
				} else {
					totalSuccess.Add(1)
				}
				count++
				totalRequests.Add(1)

				if time.Since(start) >= duration {
					return
				}
			}
		}()
	}

	wg.Wait()
	qps := float64(count) / duration.Seconds()
	fmt.Printf("  请求数: %d, QPS: %.0f\n", count, qps)
}

func testEmitAll(name string, duration time.Duration) {
	fmt.Printf("\n[%s] 测试 %v\n", name, duration)
	client := event.Dial("127.0.0.1:8080",
		event.ClientOptions().
			SetName("stress-client").
			SetSecret("test-secret").
			SetIsWrapError(false),
	)
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	var count int64
	ticker := time.NewTicker(duration)
	defer ticker.Stop()

	workers := 10
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if err := client.EmitAll("event/query", 456); err != nil {
					totalErrors.Add(1)
				} else {
					totalSuccess.Add(1)
				}
				count++
				totalRequests.Add(1)

				if time.Since(start) >= duration {
					return
				}
			}
		}()
	}

	wg.Wait()
	qps := float64(count) / duration.Seconds()
	fmt.Printf("  请求数: %d, QPS: %.0f\n", count, qps)
}

func testEmitFirst(name string, duration time.Duration) {
	fmt.Printf("\n[%s] 测试 %v\n", name, duration)
	client := event.Dial("127.0.0.1:8080",
		event.ClientOptions().
			SetName("stress-client").
			SetSecret("test-secret").
			SetIsWrapError(false),
	)
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	var count int64
	ticker := time.NewTicker(duration)
	defer ticker.Stop()

	workers := 10
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if err := client.EmitFirst("event/update", 789); err != nil {
					totalErrors.Add(1)
				} else {
					totalSuccess.Add(1)
				}
				count++
				totalRequests.Add(1)

				if time.Since(start) >= duration {
					return
				}
			}
		}()
	}

	wg.Wait()
	qps := float64(count) / duration.Seconds()
	fmt.Printf("  请求数: %d, QPS: %.0f\n", count, qps)
}

func runStressTest(name string, duration time.Duration, testFunc func(*event.Client, int)) {
	fmt.Printf("\n[%s] 长时间压测 %v\n", name, duration)
	client := event.Dial("127.0.0.1:8080",
		event.ClientOptions().
			SetName("stress-client-long").
			SetSecret("test-secret").
			SetIsWrapError(false),
	)
	time.Sleep(100 * time.Millisecond)

	workers := 50
	testFunc(client, workers)
}

func stressEmitOne(client *event.Client, workers int) {
	var count int64
	start := time.Now()
	duration := 10 * time.Second

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			localCount := 0
			for {
				if err := client.EmitOne("event/delete", workerID); err != nil {
					totalErrors.Add(1)
				} else {
					totalSuccess.Add(1)
				}
				localCount++
				totalRequests.Add(1)

				if time.Since(start) >= duration {
					atomic.AddInt64(&count, int64(localCount))
					return
				}
			}
		}(i)
	}

	wg.Wait()
	qps := float64(count) / duration.Seconds()
	fmt.Printf("  请求数: %d, QPS: %.0f\n", count, qps)
}
