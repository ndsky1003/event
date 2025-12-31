package main

import (
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ndsky1003/event/v2"
	"github.com/ndsky1003/event/v2/eventname"
)

var (
	totalRequests atomic.Int64
	totalSuccess atomic.Int64
	totalErrors  atomic.Int64
	activeWorkers atomic.Int64
)

func main() {
	// 启动 pprof
	go func() {
		log.Println(http.ListenAndServe("localhost:6060", nil))
	}()

	// 启动服务器
	srv := event.NewServer(
		event.ServerOptions().
			SetSecret("").
			SetTimeout(3*time.Second).
			SetIsWrapError(false),
	)
	srv.Listen("127.0.0.1:8080")
	log.Println("Server started on :8080")

	time.Sleep(500 * time.Millisecond)

	// 启动高性能监听器
	numListeners := 10
	for i := 0; i < numListeners; i++ {
		go startHighPerfListener(i)
	}
	log.Printf("Started %d high-perf listeners\n", numListeners)
	// 等待所有 listener 连接并注册完成
	time.Sleep(2 * time.Second)

	fmt.Println("\n==========================================")
	fmt.Println("        高性能极限 QPS 测试")
	fmt.Println("==========================================")

	// 测试不同配置
	runBenchmark("1 Worker-5s", 1, 5*time.Second)
	runBenchmark("10 Workers-5s", 10, 5*time.Second)
	runBenchmark("50 Workers-5s", 50, 5*time.Second)
	runBenchmark("100 Workers-5s", 100, 5*time.Second)
	runBenchmark("200 Workers-5s", 200, 5*time.Second)

	// 极限测试 - 长时间
	runBenchmark("500 Workers-30s", 500, 30*time.Second)

	fmt.Println("\n==========================================")
	fmt.Println("              最终统计")
	fmt.Println("==========================================")
	fmt.Printf("总请求数: %d\n", totalRequests.Load())
	fmt.Printf("成功数:   %d\n", totalSuccess.Load())
	fmt.Printf("错误数:   %d\n", totalErrors.Load())
}

// FastHandler 使用 Handler 接口的零反射处理器
type FastHandler struct {
	id int
}

func (h *FastHandler) Handle(params ...any) error {
	// 快速路径 - 最小化处理
	if len(params) > 0 {
		_ = params[0]
	}
	return nil
}

func startHighPerfListener(id int) {
	client := event.Dial("127.0.0.1:8080",
		event.ClientOptions().
			SetName(fmt.Sprintf("fast-listener-%d", id)).
			SetIsWrapError(false),
	)

	// 使用 Handler 接口 - 零反射路径
	handler := &FastHandler{id: id}

	events := []eventname.T{
		"fast/event/a",
		"fast/event/b",
		"fast/event/c",
		"fast/event/d",
		"fast/event/e",
	}

	for _, evt := range events {
		client.OnHandler(evt, handler)
	}

	log.Printf("Fast listener %d ready (Handler interface, zero reflection)", id)
}

func runBenchmark(name string, workers int, duration time.Duration) {
	fmt.Printf("\n[%s] %d workers, %v\n", name, workers, duration)

	activeWorkers.Store(int64(workers))

	// 使用共享 Client 池，避免过多连接
	numClients := min(workers, 30) // 最多30个连接
	clients := make([]*event.Client, numClients)
	for i := 0; i < numClients; i++ {
		clients[i] = event.Dial("127.0.0.1:8080",
			event.ClientOptions().
				SetName(fmt.Sprintf("client-%d", i)).
				SetIsWrapError(false),
		)
	}
	// 等待连接建立
	time.Sleep(time.Second)

	// 先启动所有 worker，等待连接建立
	startCh := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			client := clients[workerID%numClients]
			workerLoop(workerID, duration, startCh, client)
		}(i)
	}
	// 额外等待确保所有连接就绪
	time.Sleep(500 * time.Millisecond)

	// 开始测试
	start := time.Now()
	startCount := totalRequests.Load()
	close(startCh)
	wg.Wait()

	elapsed := time.Since(start)
	count := totalRequests.Load() - startCount
	qps := float64(count) / elapsed.Seconds()

	fmt.Printf("  完成: %v, 请求数: %d, QPS: %.0f\n",
		elapsed.Round(time.Millisecond), count, qps)
}

func workerLoop(workerID int, duration time.Duration, startCh chan struct{}, client *event.Client) {
	// 等待开始信号
	<-startCh

	start := time.Now()
	count := int64(0)

	// 使用事件类型轮询，避免热点
	events := []eventname.T{
		"fast/event/a",
		"fast/event/b",
		"fast/event/c",
		"fast/event/d",
		"fast/event/e",
	}
	eventCount := len(events)

	batchSize := 100
	for time.Since(start) < duration {
		for i := 0; i < batchSize; i++ {
			evt := events[count%int64(eventCount)]
			// 同步发送，等待处理完成（真实QPS）
			if err := client.EmitOne(evt, count); err != nil {
				totalErrors.Add(1)
			} else {
				totalSuccess.Add(1)
			}
			totalRequests.Add(1)
			count++
		}
	}
}

