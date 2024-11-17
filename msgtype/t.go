package msgtype

import "fmt"

type T uint8

const (
	Invalid T = iota
	Ping      //心跳维护
	Pong
	Verify
	On         //监听事件
	Req        //请求,默认响应所有的,所有均没error,才代表这次调用成功了
	ReqSomeOne //请求,一堆多的情况下,默认响应最快的那个结果
	Res        //响应
	ResSomeOne //响应
)

var m = map[T]string{
	Invalid:    "Invalid",
	Ping:       "Ping",
	Pong:       "Pong",
	Verify:     "Verify",
	On:         "On",
	Req:        "Req",
	ReqSomeOne: "ReqSomeOne",
	Res:        "Res",
	ResSomeOne: "ResSomeOne",
}

func (this T) String() string {
	if s, ok := m[this]; ok {
		return s
	} else {
		return fmt.Sprintf("未知:%d", uint8(this))
	}
}
