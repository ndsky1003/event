package codec

import "github.com/ndsky1003/event/msg"

// 解码器
type Codec interface {
	Read(*msg.Msg) error
	Write(*msg.Msg) error
	Close() error
}
