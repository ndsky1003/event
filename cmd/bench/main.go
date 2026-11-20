// 事件总线分进程压测：服务端、监听者、发布者跑在三个独立进程里，避免单进程自干扰。
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

	"github.com/ndsky1003/event/v2"
	"github.com/ndsky1003/event/v2/eventname"
)

func main() {
	mode := flag.String("mode", "", "server | listener | publisher")
	addr := flag.String("addr", ":8080", "服务端地址（支持 unix:// 前缀）")
	topic := flag.String("topic", "bench", "事件名")
	count := flag.Int("count", 1, "监听者/发布者数量")
	reqs := flag.Int("reqs", 20000, "每个发布者的请求数")
	async := flag.Int("async", 0, ">0 用 EmitAsync 且窗口为 N；0 用同步 Emit")
	flag.Parse()

	switch *mode {
	case "server":
		runServer(*addr)
	case "listener":
		runListener(*addr, *topic, *count)
	case "publisher":
		runPublisher(*addr, *topic, *count, *reqs, *async)
	default:
		log.Fatal("需要 -mode server|listener|publisher")
	}
}

func runServer(addr string) {
	srv := event.NewServer()
	go srv.Listen(addr)
	fmt.Printf("server listening %s\n", addr)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	_ = srv.Shutdown()
}

func runListener(addr, topic string, count int) {
	for i := 0; i < count; i++ {
		go func() {
			c := event.Dial(addr, event.ClientOptions())
			defer c.Close()
			if err := c.On(eventname.T(topic), func() error { return nil }); err != nil {
				log.Printf("On err: %v", err)
				return
			}
			select {} // 阻塞保持连接，等 Ctrl-C
		}()
	}
	fmt.Printf("%d 个监听者已注册 %q，等发布者…\n", count, topic)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
}

func runPublisher(addr, topic string, count, reqs, async int) {
	start := time.Now()
	var wg sync.WaitGroup
	var total atomic.Int64
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := event.Dial(addr, event.ClientOptions())
			defer c.Close()
			// Dial 是异步的，先预热等连接建立、监听者注册，再正式压测
			warmup(c, eventname.T(topic))
			if async > 0 {
				total.Add(int64(publishAsync(c, eventname.T(topic), reqs, async)))
			} else {
				total.Add(int64(publishSync(c, eventname.T(topic), reqs)))
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	qps := float64(total.Load()) / elapsed.Seconds()
	mode := "sync"
	if async > 0 {
		mode = fmt.Sprintf("async-%d", async)
	}
	fmt.Printf("%s: %d 发布者 × %d 请求 = %d 次，耗时 %v，QPS %.0f\n",
		mode, count, reqs, total.Load(), elapsed.Round(time.Millisecond), qps)
}

func warmup(c *event.Client, topic eventname.T) {
	for i := 0; i < 200; i++ {
		if err := c.Emit(topic); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func publishSync(c *event.Client, topic eventname.T, reqs int) int {
	n := 0
	for i := 0; i < reqs; i++ {
		if err := c.Emit(topic); err != nil {
			log.Printf("emit err: %v", err)
			break
		}
		n++
	}
	return n
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
