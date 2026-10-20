//go:generate msgp -tests=false

package msgtype

import "fmt"

type T uint8

const (
	Invalid T = iota
	// Ping / Pong 已经不再使用：心跳与断连检测下沉到了传输层（conn.go 的
	// flag_ping / flag_pong 帧），不再走应用层消息。
	// 枚举值保留原位是为了不改变后面几项的线上取值——删掉会让 Verify 从 3 变 1。
	Ping
	Pong
	Verify
	On         //监听事件
	Req        //请求,默认响应所有的,所有均没error,才代表这次调用成功了
	ReqRandOne //请求,一堆多的情况下,默认响应最快的那个结果
	Res        //响应
	ResRandOne //响应
)

var m = map[T]string{
	Invalid:    "Invalid",
	Ping:       "Ping",
	Pong:       "Pong",
	Verify:     "Verify",
	On:         "On",
	Req:        "Req",
	ReqRandOne: "ReqRandOne",
	Res:        "Res",
	ResRandOne: "ResRandOne",
}

func (this T) String() string {
	if s, ok := m[this]; ok {
		return s
	} else {
		return fmt.Sprintf("未知:%d", uint8(this))
	}
}
