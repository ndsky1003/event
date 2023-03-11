package msg

import (
	"fmt"

	"github.com/sirupsen/logrus"
)

type MsgType int

const (
	MsgType_invalid MsgType = 0
	MsgType_ping    MsgType = 1 << (iota - 1) //心跳维护
	MsgType_pong
	MsgType_varify
	MsgType_prepared //服务端已经准备好了
	MsgType_on       //监听事件
	MsgType_req      //请求
	MsgType_res      //响应
)

var msgTypeString = map[MsgType]string{
	MsgType_invalid:  "invalid",
	MsgType_ping:     "Ping",
	MsgType_pong:     "Pong",
	MsgType_varify:   "varify",
	MsgType_prepared: "Prepared",
	MsgType_on:       "on",
	MsgType_req:      "req",
	MsgType_res:      "res",
}

func (this MsgType) String() string {
	if s, ok := msgTypeString[this]; ok {
		return s
	} else {
		return fmt.Sprintf("未知:(%d)", this)
	}
}

type Msg struct {
	T         MsgType
	ServerSeq uint64 //服务器请求的序号 ,响应的时候用
	LocalSeq  uint64 //本地请求序号
	Name      string
	EventType
	BodyCount int8 // 超过这个数就是自讨苦吃
	Bytes     []byte
	Error     string // error
}

func (this *Msg) String() string {
	if this == nil {
		return ""
	}
	return fmt.Sprintf("Msg:T:%v,ServerSeq:%d,LocalSeq:%d,Name:%s,EventType:%v,BodyCount:%d,Error:%v", this.T, this.ServerSeq, this.LocalSeq, this.Name, this.EventType, this.BodyCount, this.Error)
}

type Call struct {
	Msg   *Msg
	Done  chan *Call
	Error error
}

func NewCall(m *Msg) *Call {
	if m == nil {
		return nil
	}
	return &Call{
		Msg:  m,
		Done: make(chan *Call, 1),
	}
}

func (this *Call) Do() {
	if this == nil {
		return
	}
	select {
	case this.Done <- this:
	default:
		logrus.Errorf("not handle here :%+v", this.Msg)
	}
}
