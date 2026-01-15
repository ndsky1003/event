//go:generate msgp --tests=false
package msg

import (
	"fmt"

	"github.com/ndsky1003/event/v3/eventname"
	"github.com/ndsky1003/event/v3/msgtype"
)

type MsgVerifyReq struct {
	Name   string
	Secret string
}

type MsgVerifyRes struct {
	Err string
}

//msgp:replace eventname.T with:string
//msgp:replace msgtype.T with:uint8
type Msg struct {
	T         msgtype.T
	EventName eventname.T
	Seq       uint64 //本地请求序号
	Name      string
	BodyCount int8 // 超过这个数就是自讨苦吃
	Bytes     []byte
	Err       string // error
}

func (this *Msg) String() string {
	if this == nil {
		return ""
	}
	return fmt.Sprintf("%+v", *this)
}

func (this *Msg) Clear() {
	this.T = msgtype.Invalid
	this.EventName = ""
	this.Seq = 0
	this.Name = ""
	this.BodyCount = 0
	clear(this.Bytes)
	this.Err = ""
}
