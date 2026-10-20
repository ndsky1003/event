//go:generate msgp -tests=false

package msg

import (
	"fmt"

	"github.com/ndsky1003/event/v2/coder"
	"github.com/ndsky1003/event/v2/eventname"
	"github.com/ndsky1003/event/v2/msgtype"
)

// 下面这些 msgpack tag 是线上字段名，不是给反射库看的装饰：
// msgpack map 格式每条消息都要带字段名，用短名一条能省下 50 字节上下。
// 反射路径（vmihailenco/msgpack）和 msgp codegen 生成的代码都认 msgpack 这个
// tag key（实测：msgp 忽略 msgp:"..." 和 json:"..."，只认 msgpack:"..."），
// 所以给这些类型跑 codegen 不会改变线上格式。
//
// 注意：改 tag 等于改协议。字段名对不上时解码端不会报错，只会静默丢字段
// （msgpack 默认忽略未知 key），所以两端必须来自同一份源码。
type MsgVerifyReq struct {
	Name   string `msgpack:"n"`
	Secret string `msgpack:"s"`
}

type MsgVerifyRes struct {
	Err string `msgpack:"e"`
}

type Msg struct {
	T         msgtype.T   `msgpack:"t"`
	EventName eventname.T `msgpack:"en"`
	Seq       uint64      `msgpack:"q"` //本地请求序号
	Name      string      `msgpack:"n"`
	BodyCount int8        `msgpack:"bc"` // 超过这个数就是自讨苦吃
	Coder     coder.T     `msgpack:"c"`  //事件参数的编码方式（Raw/JSON/Msgp）
	Bytes     []byte      `msgpack:"b"`
	Err       string      `msgpack:"e"` // error
}

func (this *Msg) String() string {
	if this == nil {
		return ""
	}
	return fmt.Sprintf("%+v", *this)
}
