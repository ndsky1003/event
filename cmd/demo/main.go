// p2p 事件总线演示：一个进程内起服务端节点 + 3 个订阅者 + 1 个发布者，
// 模拟「充值事件解耦」——充值事件广播给所有订阅的进程。
//
// 运行：go run ./cmd/demo
package main

import (
	"fmt"
	"time"

	"github.com/ndsky1003/event/v3"
	"github.com/ndsky1003/event/v3/eventname"
)

const serverAddr = "127.0.0.1:18080"

func main() {
	// 1. 服务端节点：只做节点发现，不参与事件路由
	dir := event.NewServer()
	go dir.Listen(serverAddr)
	time.Sleep(100 * time.Millisecond)
	fmt.Println("服务端节点已启动:", serverAddr)

	// 2. 账号服务：订阅充值事件，加余额
	account := event.Dial(serverAddr, event.ClientOptions().
		SetName("account-service").SetListenAddr("127.0.0.1:0"))
	defer account.Close()
	if err := account.On("recharge", func(user string, amount int) error {
		fmt.Printf("  [account] 用户 %s 充值 %d 元 -> 加余额\n", user, amount)
		return nil
	}); err != nil {
		panic(err)
	}

	// 3. 积分服务：也订阅充值事件，发积分
	coupon := event.Dial(serverAddr, event.ClientOptions().
		SetName("coupon-service"))
	defer coupon.Close()
	if err := coupon.On("recharge", func(user string, amount int) error {
		fmt.Printf("  [coupon]  用户 %s 充值 %d 元 -> 发积分\n", user, amount)
		return nil
	}); err != nil {
		panic(err)
	}

	// 4. 风控服务：订阅充值事件，大额返回错误（演示错误回传）
	risk := event.Dial(serverAddr, event.ClientOptions().
		SetName("risk-service"))
	defer risk.Close()
	if err := risk.On("recharge", func(user string, amount int) error {
		if amount > 300 {
			return fmt.Errorf("金额 %d 超过风控阈值", amount)
		}
		fmt.Printf("  [risk]   用户 %s 充值 %d 元 -> 风控通过\n", user, amount)
		return nil
	}); err != nil {
		panic(err)
	}

	// 5. 订单服务：发布充值事件
	order := event.Dial(serverAddr, event.ClientOptions().
		SetName("order-service"))
	defer order.Close()

	// 等订阅同步（跨节点订阅是异步的），确认 3 个订阅者都已连上
	waitSub(order, "recharge", 3)

	fmt.Println("\n开始发布充值事件……")
	for i := 1; i <= 5; i++ {
		user := fmt.Sprintf("user-%d", i)
		amount := i * 100
		fmt.Printf(">>> [order] 发布充值: %s %d 元\n", user, amount)
		if err := order.Emit("recharge", user, amount); err != nil {
			fmt.Printf("    Emit 聚合错误: %v\n", err)
		}
		time.Sleep(300 * time.Millisecond)
	}

	fmt.Println("演示结束")
}

// waitSub 等订阅同步到位：SubscriberCount 达到 want 个订阅者。
func waitSub(c *event.Client, en eventname.T, want int) {
	for i := 0; i < 200; i++ {
		if c.SubscriberCount(en) >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	fmt.Printf("警告：订阅同步未完成，当前 %d/%d\n", c.SubscriberCount(en), want)
}
