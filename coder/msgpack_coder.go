package coder

import (
	"bytes"
	"reflect"

	"github.com/tinylib/msgp/msgp"
	"github.com/vmihailenco/msgpack/v5"
)

// msgpack_coder 是事件参数的 msgpack 编解码器：优先走 msgp 快路径，否则回退到 msgpack 反射编码。
type msgpack_coder struct{}

// NewMsgpack 返回一个 msgpack 编解码器。
func new_msgpack_coder() *msgpack_coder { return &msgpack_coder{} }

// Encode 编码单个参数 v 追加到 b，返回追加后的切片。
// msgp 快路径：m.MarshalMsg(b) 直接 append（b 容量够则零分配）；
// nil 指针及未 codegen 的类型回退到 vmihailenco/msgpack（经 sliceWriter 直接 append 进 b）。
// 两者均产出标准 MessagePack 字节（struct 默认 map 格式），可互通。
func (c *msgpack_coder) Encode(b []byte, v any) ([]byte, error) {
	if m, ok := v.(msgp.Marshaler); ok && !isNilPtr(v) {
		return m.MarshalMsg(b)
	}
	w := sliceWriter{b: b}
	enc := msgpack.GetEncoder()
	enc.Reset(&w)
	err := enc.Encode(v)
	msgpack.PutEncoder(enc)
	if err != nil {
		return nil, err
	}
	return w.b, nil
}

// sliceWriter 实现 io.Writer，让 msgpack 直接把字节 append 进目标 []byte。
// vmihailenco/msgpack 内部无缓冲、直接流式写 writer，因此不再经过 bytes.Buffer 中转，
// 用户态编码结果零额外 copy（仅 append 到 b，容量够则零分配）。
type sliceWriter struct {
	b []byte
}

func (w *sliceWriter) Write(p []byte) (int, error) {
	w.b = append(w.b, p...)
	return len(p), nil
}

// Decode 从 b 解码一个参数到 v，返回剩余字节。
// 不能用 msgp.Decodable（DecodeMsg 内部走 fwd.Reader 缓冲，连续解码会预读丢字节），
// 必须用 msgp.Unmarshaler（UnmarshalMsg 基于 []byte、返回剩余字节）。
// msgpack 回退路径用 bytes.Reader 做零拷贝视图（bytes.Reader 实现 io.ByteScanner，
// 命中 msgpack.ResetReader 的 bufReader 快路径，不引入 bufio 缓冲），解码不复制输入字节。
func (c *msgpack_coder) Decode(b []byte, v any) ([]byte, error) {
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
