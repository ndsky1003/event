// 事件总线分进程压测：服务端节点、监听者、发布者跑在三个独立进程里，避免单进程自干扰。
//
// 用法（三个终端）：
//
//	bench -mode server -addr :18080
//	bench -mode listener -addr :18080 -topic bench -count 4
//	bench -mode publisher -addr :18080 -topic bench -count 16 -reqs 20000 [-async 256]
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ndsky1003/event/v3"
	"github.com/ndsky1003/event/v3/eventname"
)

func main() {
	mode := flag.String("mode", "", "server | listener | publisher")
	addr := flag.String("addr", ":8080", "服务端节点地址（支持 unix:// 前缀）")
	topic := flag.String("topic", "bench", "事件名")
	count := flag.Int("count", 1, "监听者/发布者数量")
	subs := flag.Int("subs", 1, "发布者 warmup 等待的订阅者数量（应等于 listener 的 -count）")
	reqs := flag.Int("reqs", 20000, "每个发布者的请求数")
	async := flag.Int("async", 0, ">0 用 EmitAsync 且窗口为 N；0 用同步 Emit")
	flag.Parse()

	switch *mode {
	case "server":
		runServer(*addr)
	case "listener":
		runListener(*addr, *topic, *count)
	case "publisher":
		runPublisher(*addr, *topic, *count, *subs, *reqs, *async)
	default:
		log.Fatal("需要 -mode server|listener|publisher")
	}
}

func runServer(addr string) {
	dir := event.NewServer()
	go dir.Listen(addr)
	fmt.Printf("server listening %s\n", addr)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	_ = dir.Shutdown()
}

func runListener(serverAddr, topic string, count int) {
	for i := 0; i < count; i++ {
		go func(i int) {
			name := fmt.Sprintf("listener-%d", i)
			listen := fmt.Sprintf("unix:///tmp/event-bench-%s.sock", name)
			c := event.Dial(serverAddr, event.ClientOptions().SetName(name).SetListenAddr(listen))
			defer c.Close()
			if err := c.On(eventname.T(topic), func() error { return nil }); err != nil {
				log.Printf("On err: %v", err)
				return
			}
			select {} // 阻塞保持连接，等 Ctrl-C
		}(i)
	}
	fmt.Printf("%d 个监听者已注册 %q，等发布者…\n", count, topic)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
}

func runPublisher(serverAddr, topic string, count, subs, reqs, async int) {
	start := time.Now()
	var wg sync.WaitGroup
	var total atomic.Int64
	var latencyNs atomic.Int64 // 成功请求的累计耗时（同步模式才有意义）
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("publisher-%d", i)
			listen := fmt.Sprintf("unix:///tmp/event-bench-%s.sock", name)
			c := event.Dial(serverAddr, event.ClientOptions().SetName(name).SetListenAddr(listen))
			defer c.Close()
			// Dial 是异步的：等连接建立、订阅同步到位，再正式压测
			warmup(c, eventname.T(topic), subs)
			if async > 0 {
				total.Add(int64(publishAsync(c, eventname.T(topic), reqs, async)))
			} else {
				n, ns := publishSync(c, eventname.T(topic), reqs)
				total.Add(int64(n))
				latencyNs.Add(ns)
			}
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)
	totalN := total.Load()
	qps := float64(totalN) / elapsed.Seconds()
	mode := "sync"
	if async > 0 {
		mode = fmt.Sprintf("async-%d", async)
	}
	fmt.Printf("%s: %d 发布者 × %d 请求 = %d 次，耗时 %v，QPS %.0f",
		mode, count, reqs, totalN, elapsed.Round(time.Millisecond), qps)
	if async == 0 && totalN > 0 {
		avg := time.Duration(latencyNs.Load() / totalN)
		fmt.Printf("，平均单次往返 %v", avg.Round(time.Microsecond))
	}
	fmt.Println()
}

// warmup 等订阅同步到位（SubscriberCount 达到 subs），避免把「无人监听的空转」算进压测。
func warmup(c *event.Client, topic eventname.T, subs int) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if c.SubscriberCount(topic) >= subs {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	log.Printf("warmup 超时：订阅者仅 %d/%d", c.SubscriberCount(topic), subs)
}

func publishSync(c *event.Client, topic eventname.T, reqs int) (n int, latencyNs int64) {
	for i := 0; i < reqs; i++ {
		st := time.Now()
		if err := c.Emit(topic); err != nil {
			log.Printf("emit err: %v", err)
			break
		}
		n++
		latencyNs += time.Since(st).Nanoseconds()
	}
	return
}

func publishAsync(c *event.Client, topic eventname.T, reqs, window int) int {
	sem := make(chan struct{}, window)
	var wg sync.WaitGroup
	var n atomic.Int64
	for i := 0; i < reqs; i++ {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			call := c.EmitAsync(topic)
			<-call.Done
			if call.Err == nil {
				n.Add(1)
			} else {
				log.Printf("emit async err: %v", call.Err)
			}
		}()
	}
	wg.Wait()
	return int(n.Load())
}
