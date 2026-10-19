package coder

import "fmt"

// raw_coder 不做编解码，参数以原始 []byte 透传。
// Encode 要求 v 为 []byte，直接追加到 b；Decode 要求 v 为 *[]byte，把剩余字节零拷贝赋给它。
// 由于 Decode 会消费所有剩余字节，Raw 适合作为单个参数或最后一个参数使用。
type raw_coder struct{}

func new_raw_coder() *raw_coder { return &raw_coder{} }

func (c *raw_coder) Encode(b []byte, v any) ([]byte, error) {
	raw, ok := v.([]byte)
	if !ok {
		return nil, fmt.Errorf("raw_coder encode need []byte, got %T", v)
	}
	return append(b, raw...), nil
}

func (c *raw_coder) Decode(b []byte, v any) ([]byte, error) {
	dst, ok := v.(*[]byte)
	if !ok {
		return nil, fmt.Errorf("raw_coder decode need *[]byte, got %T", v)
	}
	*dst = b
	return nil, nil
}
