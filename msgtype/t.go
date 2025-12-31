package msgtype

import "fmt"

type T uint8

const (
	Invalid T = iota
	Verify
	On         //监听事件
	ReqAll     //请求所有监听者,等待所有响应,收集所有错误
	ReqOne     //随机发送给一个监听者
	ReqFirst   //发送给所有监听者,只接受第一个返回值
	Res        //响应
	ResFirst   //第一个响应
)

var m = map[T]string{
	Invalid:  "Invalid",
	Verify:   "Verify",
	On:       "On",
	ReqAll:   "ReqAll",
	ReqOne:   "ReqOne",
	ReqFirst: "ReqFirst",
	Res:      "Res",
	ResFirst: "ResFirst",
}

func (this T) String() string {
	if s, ok := m[this]; ok {
		return s
	} else {
		return fmt.Sprintf("未知:%d", uint8(this))
	}
}
