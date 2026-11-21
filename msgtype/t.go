//go:generate msgp -tests=false

package msgtype

import "fmt"

type T uint8

const (
	Invalid T = iota
	// Ping / Pong 已经不再使用：心跳与断连检测下沉到了传输层（conn.go 的
	// flag_ping / flag_pong 帧），不再走应用层消息。
	// 枚举值保留原位是为了不改变后面几项的线上取值——删掉会让 Verify 从 3 变 1。
	Verify
	On         //监听事件
	Req        //请求,默认响应所有的,所有均没error,才代表这次调用成功了
	ReqRandOne //请求,一堆多的情况下,默认响应最快的那个结果
	Res        //响应
	ResRandOne //响应
	// 下面是 p2p 模式新增的消息类型，追加在末尾不影响已有取值。
	Register    //节点注册（节点 → 目录），携带监听地址
	RegisterRes //注册响应（目录 → 节点），携带当前节点列表
	NodeDown    //节点下线通知（目录 → 节点）
	Subscribe   //订阅广播（节点 → 节点）："我订阅了 X"
	Unsubscribe //取消订阅广播（节点 → 节点）："我取消了订阅 X"
)

var m = [...]string{
	Invalid:     "Invalid",
	Verify:      "Verify",
	On:          "On",
	Req:         "Req",
	ReqRandOne:  "ReqRandOne",
	Res:         "Res",
	ResRandOne:  "ResRandOne",
	Register:    "Register",
	RegisterRes: "RegisterRes",
	NodeDown:    "NodeDown",
	Subscribe:   "Subscribe",
	Unsubscribe: "Unsubscribe",
}

func (this T) String() string {
	if int(this) < len(m) && m[this] != "" {
		return m[this]
	}
	return fmt.Sprintf("未知:%d", uint8(this))
}
