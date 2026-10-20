//go:generate msgp -tests=false

package coder

import (
	"errors"
	"fmt"
)

// Coder 是事件参数的编解码器接口。
//
// Encode 把单个参数 v 追加编码到 b，返回追加后的切片；
// Decode 从 b 解码一个参数到 v，返回剩余字节。
//
// 实现方需保证：用同一 Coder 连续 Encode 出的一批字节，能被同一 Coder
// 连续 Decode 按序还原（每个参数一一对应）。编码端与解码端必须使用一致的
// 线协议（如 msgpack、json 等）。
type Coder interface {
	Encode(b []byte, v any) ([]byte, error)
	Decode(b []byte, v any) ([]byte, error)
}

type T uint8

const (
	Raw T = iota
	JSON
	Msgp
)

func (t T) String() string {
	switch t {
	case Raw:
		return "Raw"
	case JSON:
		return "JSON"
	case Msgp:
		return "Msgp"
	default:
		return "unknown"
	}
}

func RegisterCoder(t T, coder Coder) {
	coders[t] = coder
}

var coders = map[T]Coder{
	Raw:  new_raw_coder(),
	JSON: new_json_coder(),
	Msgp: new_msgpack_coder(),
}

// Encode 根据类型标记 t 选择 coder，把 v 追加编码到 data 并返回。
func Encode(t T, data []byte, v any) (ret []byte, err error) {
	coder, ok := coders[t]
	if !ok {
		err = fmt.Errorf("coder:%v is not exist", t)
		return
	}
	return coder.Encode(data, v)
}

// Unmarshal 根据类型标记 t 选择 coder，从 data 解码一个参数到 v，返回剩余字节。
func Unmarshal(t T, data []byte, v any) (ret []byte, err error) {
	if v == nil {
		err = errors.New("decode obj v is nil")
		return
	}
	coder, ok := coders[t]
	if !ok {
		err = fmt.Errorf("coder:%v is not exist", t)
		return
	}
	return coder.Decode(data, v)
}
