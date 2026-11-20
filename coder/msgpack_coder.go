package coder

// 本文件是事件参数的 msgpack 编解码器，内部有两条路径：
//
//   - msgp 快路径：v 实现 tinylib/msgp 的 msgp.Marshaler / msgp.Unmarshaler
//     （业务方 codegen 生成）时走这条，基于 []byte、零分配。
//   - msgpack 反射回退：其余类型走 vmihailenco/msgpack 反射编码。
//
// 两条路径产出的都是标准 MessagePack 字节（struct 默认 map 格式），因此可互通：
// 一个进程用 msgp 编码、另一个进程用 msgpack 解码，或反之。
//
// 互通的前提与约定：
//
//  1. struct 必须都用 map 格式（msgp 默认、msgpack 默认都是 map），且字段名（map key）
//     两端对齐：msgp codegen 认 msgpack:"..." tag，msgpack 反射默认用字段名。
//
//  2. 可 nil 的容器字段（map / slice / []byte）必须加 allownil tag，例如：
//
//     type Foo struct {
//     M map[string]int `msgpack:"m,allownil"`
//     S []string       `msgpack:"s,allownil"`
//     B []byte         `msgpack:"b,allownil"`
//     }
//
//     原因：msgpack 反射把 nil 容器编码为 nil（0xc0），而 msgp codegen 默认把 nil 容器
//     编码为 empty（len(nil)==0），且解码时 ReadMapHeaderBytes / ReadArrayHeaderBytes
//     无法读 nil。加了 allownil 后，msgp 对 nil 容器编码为 nil、解码也能读 nil，
//     与 msgpack 反射对齐，双向互通才成立；否则「msgpack 编码的 nil 容器交给 msgp 解码」
//     会报错（见 msgpack_msgp_interop_test.go 的 TestMsgpackMsgpNilContainer）。

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
