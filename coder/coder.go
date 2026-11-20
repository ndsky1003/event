//go:generate msgp -tests=false

package coder

import (
	"errors"
	"fmt"
	"reflect"
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

// Max 是 coders 数组容量，也是可注册 coder 类型的上界（合法 t ∈ [0, Max)）。
// 内置 Raw/JSON/Msgp 占 0..2，预留 T(3)..T(9) 给自定义 coder；
// 新增内置类型时需保证 Max 大于最后一个内置类型。
const Max T = 10

func (t T) String() string {
	switch t {
	case Raw:
		return "Raw"
	case JSON:
		return "JSON"
	case Msgp:
		return "Msgp"
	default:
		return "Unknown"
	}
}

// RegisterCoder 注册（或覆盖）类型 t 对应的 coder。
//
// 必须在启动阶段、任何 Encode/Decode 调用之前完成，运行期不可并发调用，
// 否则会与 Encode/Decode 对 coders 的读写构成数据竞争。
func RegisterCoder(t T, coder Coder) error {
	if t >= Max {
		return fmt.Errorf("invalid t:%d", t)
	}
	// !rv.IsValid() 拦截 nil 接口，rv.Kind()==Pointer && rv.IsNil() 拦截类型化的 nil 指针。
	if rv := reflect.ValueOf(coder); !rv.IsValid() || (rv.Kind() == reflect.Pointer && rv.IsNil()) {
		return errors.New("coder is nil")
	}
	coders[t] = coder
	return nil
}

var coders = [Max]Coder{
	Raw:  new_raw_coder(),
	JSON: new_json_coder(),
	Msgp: new_msgpack_coder(),
}

// getCoder 返回类型 t 对应的 coder；t 越界或未注册时返回错误。
func getCoder(t T) (Coder, error) {
	if t >= Max {
		return nil, fmt.Errorf("coder:%d does not exist", t)
	}
	if coder := coders[t]; coder != nil {
		return coder, nil
	}
	return nil, fmt.Errorf("coder:%d does not exist", t)
}

// Encode 根据类型标记 t 选择 coder，把 v 追加编码到 data 并返回。
func Encode(t T, data []byte, v any) (ret []byte, err error) {
	coder, err := getCoder(t)
	if err != nil {
		return
	}
	return coder.Encode(data, v)
}

// Decode 根据类型标记 t 选择 coder，从 data 解码一个参数到 v，返回剩余字节。
func Decode(t T, data []byte, v any) (ret []byte, err error) {
	if v == nil {
		err = errors.New("decode obj v is nil")
		return
	}
	coder, err := getCoder(t)
	if err != nil {
		return
	}
	return coder.Decode(data, v)
}
