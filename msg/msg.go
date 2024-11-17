package msg

import (
	"fmt"

	"github.com/ndsky1003/event/v2/eventname"
	"github.com/ndsky1003/event/v2/msgtype"
)

type MsgVerifyReq struct {
	Name   string
	Secret string
}

type MsgVerifyRes struct {
	Err string
}

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
