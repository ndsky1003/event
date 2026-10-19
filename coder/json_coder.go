package coder

import (
	"bytes"
	"encoding/json"
)

// json_coder 是事件参数的 json_coder 编解码器（NDjson_coder 风格），
// 作为接入其他 coder 的示例：只需实现 Coder 接口，即可通过依赖注入替换默认的 msgpack。
//
// 注意：json_coder 自身没有长度前缀，连续值之间需要分隔，因此 Encode 在每个值后追加换行符，
// Decode 以换行符为边界解析。同一 json_coder 实例的 Encode/Decode 需配对使用。
type json_coder struct{}

// Newjson_coder 返回一个 json_coder 编解码器。
func new_json_coder() *json_coder { return &json_coder{} }

// Encode 把 v 序列化为 json_coder 追加到 b，并在末尾追加换行符作为值边界。
func (c *json_coder) Encode(b []byte, v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	b = append(b, data...)
	return append(b, '\n'), nil
}

// Decode 从 b 解析一个换行分隔的 json_coder 值到 v，返回剩余字节。
func (c *json_coder) Decode(b []byte, v any) ([]byte, error) {
	i := bytes.IndexByte(b, '\n')
	if i < 0 {
		// 最后一个值可能没有换行符，取到末尾
		i = len(b)
	}
	if err := json.Unmarshal(b[:i], v); err != nil {
		return nil, err
	}
	if i < len(b) {
		i++ // 跳过换行符
	}
	return b[i:], nil
}
