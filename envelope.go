package event

import (
	"fmt"

	"github.com/ndsky1003/event/v2/coder"
)

// 消息框架（msg.Msg / MsgVerifyReq / MsgVerifyRes）的序列化统一走 coder，
// 默认 Msgp。原来的 codec 包（gob）已经移除。
//
// 帧 body 布局：[1 字节 coder 标记][该 coder 编码出来的结构体]
//
// 为什么要那个标记字节：服务端不必预先知道客户端用哪种 coder，握手第一帧就把它
// 带过来了，之后整条连接的回包都沿用同一个 coder。
// 事件参数自身的编码方式仍然由 msg.Msg.Coder 字段声明，两者互不影响。
const envelopeTagLen = 1

// envelopeCoder 把用户配置的 coder 收敛成"能编码消息框架"的那一种。
// coder.Raw 只透传 []byte，编不了结构体，所以框架层一律退回 Msgp。
func envelopeCoder(t coder.T) coder.T {
	if t == coder.Raw {
		return coder.Msgp
	}
	return t
}

// encodeEnvelope 返回新分配的切片：帧会在发送队列里排队，不能复用缓冲区。
// 先放标记字节再让 coder 往后面 append，全程只有一次分配。
func encodeEnvelope(t coder.T, v any) ([]byte, error) {
	t = envelopeCoder(t)
	body, err := coder.Encode(t, []byte{byte(t)}, v)
	if err != nil {
		return nil, fmt.Errorf("coder:%v encode envelope err:%w", t, err)
	}
	return body, nil
}

// decodeEnvelope 按帧里的标记字节选 coder 解码到 v，并把这个 coder 返回给调用方
// （服务端要用它回同一种编码）。
func decodeEnvelope(body []byte, v any) (coder.T, error) {
	if len(body) <= envelopeTagLen {
		return 0, fmt.Errorf("envelope too short:%d", len(body))
	}
	t := coder.T(body[0])
	if _, err := coder.Unmarshal(t, body[envelopeTagLen:], v); err != nil {
		return t, fmt.Errorf("coder:%v decode envelope err:%w", t, err)
	}
	return t, nil
}
