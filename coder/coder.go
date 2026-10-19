package coder

import (
	"bytes"
	"reflect"

	"github.com/tinylib/msgp/msgp"
	"github.com/vmihailenco/msgpack/v5"
)

// Coder 是事件参数的编解码器：优先走 msgp 快路径，否则回退到 msgpack 反射编码。
type Coder struct{}

func New() *Coder { return &Coder{} }

// Encode 编码单个参数 v 追加到 b，返回追加后的切片。
// msgp 快路径：m.MarshalMsg(b) 直接 append（b 容量够则零分配）；
// nil 指针及未 codegen 的类型回退到 vmihailenco/msgpack。
// 两者均产出标准 MessagePack 字节（struct 默认 map 格式），可互通。
func (c *Coder) Encode(b []byte, v any) ([]byte, error) {
	if m, ok := v.(msgp.Marshaler); ok && !isNilPtr(v) {
		return m.MarshalMsg(b)
	}
	var buf bytes.Buffer
	enc := msgpack.GetEncoder()
	enc.Reset(&buf)
	err := enc.Encode(v)
	msgpack.PutEncoder(enc)
	if err != nil {
		return nil, err
	}
	return append(b, buf.Bytes()...), nil
}

// Decode 从 b 解码一个参数到 v，返回剩余字节。
// 不能用 msgp.Decodable（DecodeMsg 内部走 fwd.Reader 缓冲，连续解码会预读丢字节），
// 必须用 msgp.Unmarshaler（UnmarshalMsg 基于 []byte、返回剩余字节）。
func (c *Coder) Decode(b []byte, v any) ([]byte, error) {
	if m, ok := v.(msgp.Unmarshaler); ok {
		return m.UnmarshalMsg(b)
	}
	reader := bytes.NewReader(b)
	dec := msgpack.GetDecoder()
	dec.Reset(reader)
	err := dec.Decode(v)
	msgpack.PutDecoder(dec)
	if err != nil {
		return nil, err
	}
	return b[len(b)-reader.Len():], nil
}

func isNilPtr(v any) bool {
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Pointer && rv.IsNil()
}
