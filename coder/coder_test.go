package coder

import (
	"reflect"
	"testing"

	"github.com/tinylib/msgp/msgp"
)

// ---- 测试用类型 ----

type inner struct {
	A int
	B string
}

type outer struct {
	Inner inner
	Flag  bool
}

type plain struct {
	Name string
	N    int
	Tags []string
	Meta map[string]int64
}

// msgpUser 手写实现 msgp.Marshaler / msgp.Unmarshaler（map 格式），
// 模拟业务方 msgp codegen 产物，用于覆盖 msgpack 的 msgp 快路径。
// 注意：map key 用字段名 "Name"/"Age"，与 msgpack 反射（默认无 tag 用字段名）互通。
type msgpUser struct {
	Name string
	Age  int32
}

func (u *msgpUser) MarshalMsg(b []byte) ([]byte, error) {
	b = msgp.AppendMapHeader(b, 2)
	b = msgp.AppendString(b, "Name")
	b = msgp.AppendString(b, u.Name)
	b = msgp.AppendString(b, "Age")
	b = msgp.AppendInt32(b, u.Age)
	return b, nil
}

func (u *msgpUser) UnmarshalMsg(b []byte) ([]byte, error) {
	sz, b, err := msgp.ReadMapHeaderBytes(b)
	if err != nil {
		return b, err
	}
	for i := uint32(0); i < sz; i++ {
		var key []byte
		key, b, err = msgp.ReadMapKeyZC(b)
		if err != nil {
			return b, err
		}
		switch string(key) {
		case "Name":
			u.Name, b, err = msgp.ReadStringBytes(b)
		case "Age":
			u.Age, b, err = msgp.ReadInt32Bytes(b)
		default:
			b, err = msgp.Skip(b)
		}
		if err != nil {
			return b, err
		}
	}
	return b, nil
}

// ---- Msgp 编解码一致性 ----

func TestMsgpackRoundtrip(t *testing.T) {
	cases := []struct {
		name string
		v    any
	}{
		{"string", "hello, 世界"},
		{"empty_string", ""},
		{"bytes", []byte{0x01, 0x02, 0xff, 0x00}},
		{"bool_true", true},
		{"bool_false", false},
		{"int", int(-12345)},
		{"int64_max", int64(9223372036854775807)},
		{"int64_min", int64(-9223372036854775808)},
		{"int32", int32(2147483647)},
		{"int16", int16(-32768)},
		{"int8", int8(127)},
		{"uint", uint(12345)},
		{"uint64_max", uint64(18446744073709551615)},
		{"uint32", uint32(4294967295)},
		{"uint16", uint16(65535)},
		{"uint8", uint8(255)},
		{"float64", float64(3.141592653589793)},
		{"float32", float32(1.5)},
		{"struct", plain{Name: "x", N: 7, Tags: []string{"a", "b"}, Meta: map[string]int64{"k": 1}}},
		{"nested_struct", outer{Inner: inner{A: 1, B: "b"}, Flag: true}},
		{"map_string_string", map[string]string{"a": "1", "b": "2"}},
		{"map_string_int64", map[string]int64{"a": 1, "b": -2}},
		{"slice_string", []string{"a", "b", "c"}},
		{"slice_int", []int{1, 2, 3}},
		{"slice_struct", []inner{{A: 1, B: "x"}, {A: 2, B: "y"}}},
		{"array", [3]int{1, 2, 3}},
		{"ptr_non_nil", &plain{Name: "p", N: 9}},
		{"ptr_nil", (*plain)(nil)},
		// msgp 快路径：值类型（Encode/Decode 均走 msgp）
		{"msgp_value", msgpUser{Name: "alice", Age: 30}},
		// msgp 快路径：非 nil 指针（Encode 走 msgp，Decode 因目标是 **T 走 msgpack 反射，验证互通）
		{"msgp_ptr", &msgpUser{Name: "bob", Age: 40}},
		{"msgp_ptr_nil", (*msgpUser)(nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRoundtrip(t, Msgp, tc.v)
		})
	}
}

// ---- JSON 编解码一致性 ----

func TestJSONRoundtrip(t *testing.T) {
	cases := []struct {
		name string
		v    any
	}{
		{"string", "hello, 世界"},
		{"empty_string", ""},
		{"bytes", []byte{0x01, 0x02, 0xff}},
		{"bool_true", true},
		{"bool_false", false},
		{"int", int(-12345)},
		{"int32", int32(2147483647)},
		{"int16", int16(-32768)},
		{"int8", int8(127)},
		{"uint", uint(12345)},
		{"float64", float64(3.141592653589793)},
		{"struct", plain{Name: "x", N: 7, Tags: []string{"a", "b"}, Meta: map[string]int64{"k": 1}}},
		{"nested_struct", outer{Inner: inner{A: 1, B: "b"}, Flag: true}},
		{"map_string_string", map[string]string{"a": "1", "b": "2"}},
		{"slice_string", []string{"a", "b", "c"}},
		{"slice_int", []int{1, 2, 3}},
		{"slice_struct", []inner{{A: 1, B: "x"}, {A: 2, B: "y"}}},
		{"ptr_non_nil", &plain{Name: "p", N: 9}},
		{"ptr_nil", (*plain)(nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRoundtrip(t, JSON, tc.v)
		})
	}
}

// ---- Raw 原始字节透传 ----

func TestRawRoundtrip(t *testing.T) {
	assertRoundtrip(t, Raw, []byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0xff})
}

// ---- 一批数据连续编解码 ----

// TestBatchRoundtrip 验证「一批数据 → 连续 Encode 到同一个 b → 从最终 bytes 连续 Decode 还原整批」。
func TestBatchRoundtrip(t *testing.T) {
	batch := []any{
		"batch-string",
		"",
		int64(-9223372036854775808),
		uint64(18446744073709551615),
		float64(2.718281828459045),
		bool(true),
		[]byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0xff},
		plain{Name: "batch", N: 7, Tags: []string{"a", "b", "c"}, Meta: map[string]int64{"k1": 1, "k2": 2}},
		outer{Inner: inner{A: 1, B: "inner"}, Flag: true},
		map[string]int64{"x": 1, "y": -2, "z": 3},
		[]inner{{A: 1, B: "p"}, {A: 2, B: "q"}, {A: 3, B: "r"}},
		&plain{Name: "ptr", N: 9},
		(*plain)(nil),
		msgpUser{Name: "alice", Age: 30},
		&msgpUser{Name: "bob", Age: 40},
		(*msgpUser)(nil),
		"结尾",
	}
	assertBatch(t, Msgp, batch)
}

func TestJSONBatchRoundtrip(t *testing.T) {
	batch := []any{
		"batch-string",
		"",
		int64(42),
		float64(2.718281828459045),
		bool(true),
		[]byte{0xde, 0xad, 0xbe, 0xef},
		plain{Name: "batch", N: 7, Tags: []string{"a", "b"}, Meta: map[string]int64{"k": 1}},
		map[string]string{"x": "1", "y": "2"},
		[]inner{{A: 1, B: "p"}, {A: 2, B: "q"}},
		&plain{Name: "ptr", N: 9},
		(*plain)(nil),
		"结尾",
	}
	assertBatch(t, JSON, batch)
}

// ---- 断言工具 ----

func assertRoundtrip(t *testing.T, coderType T, v any) {
	t.Helper()
	b, err := Encode(coderType, nil, v)
	if err != nil {
		t.Fatalf("Encode(%v, %T) err: %v", coderType, v, err)
	}
	dst := reflect.New(reflect.TypeOf(v))
	rest, err := Unmarshal(coderType, b, dst.Interface())
	if err != nil {
		t.Fatalf("Unmarshal(%v, %T) err: %v", coderType, v, err)
	}
	if len(rest) != 0 {
		t.Fatalf("Unmarshal(%v, %T) 剩余 %d 字节", coderType, v, len(rest))
	}
	got := dst.Elem().Interface()
	if !reflect.DeepEqual(got, v) {
		t.Fatalf("%T 往返不一致:\n got  %#v\n want %#v", v, got, v)
	}
}

func assertBatch(t *testing.T, coderType T, batch []any) {
	t.Helper()
	var b []byte
	for i, v := range batch {
		var err error
		b, err = Encode(coderType, b, v)
		if err != nil {
			t.Fatalf("Encode(%v, batch[%d] %T): %v", coderType, i, v, err)
		}
	}

	rest := b
	for i, v := range batch {
		dst := reflect.New(reflect.TypeOf(v))
		var err error
		rest, err = Unmarshal(coderType, rest, dst.Interface())
		if err != nil {
			t.Fatalf("Unmarshal(%v, batch[%d] %T): %v", coderType, i, v, err)
		}
		if got := dst.Elem().Interface(); !reflect.DeepEqual(got, v) {
			t.Fatalf("batch[%d] %T 不一致:\n got  %#v\n want %#v", i, v, got, v)
		}
	}
	if len(rest) != 0 {
		t.Fatalf("整批解码后应无剩余字节，实际 %d", len(rest))
	}
}

// ---- 性能测试 ----

// BenchmarkEncode 复用同一 buffer（模拟 emit_async 里 bodyPool 的 buf[:0]）。
func BenchmarkEncode(b *testing.B) {
	cases := []struct {
		name string
		v    any
	}{
		{"string", "hello, world, 一个中等长度的字符串"},
		{"int64", int64(123456789)},
		{"bytes", []byte("some binary payload 0123456789")},
		{"struct", plain{Name: "x", N: 7, Tags: []string{"a", "b"}, Meta: map[string]int64{"k": 1}}},
		{"map", map[string]int64{"a": 1, "b": 2, "c": 3, "d": 4}},
		{"msgp", msgpUser{Name: "alice", Age: 30}},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			buf := make([]byte, 0, 1024)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				buf, _ = Encode(Msgp, buf[:0], tc.v)
			}
			_ = buf
		})
	}
}

// BenchmarkDecode 每次 reflect.New 目标（模拟 parse 里 reflect.New(argType.at)），
// 故 allocs 数字里包含 reflect.New 的一次分配。
func BenchmarkDecode(b *testing.B) {
	cases := []struct {
		name string
		v    any
	}{
		{"string", "hello, world, 一个中等长度的字符串"},
		{"int64", int64(123456789)},
		{"bytes", []byte("some binary payload 0123456789")},
		{"struct", plain{Name: "x", N: 7, Tags: []string{"a", "b"}, Meta: map[string]int64{"k": 1}}},
		{"map", map[string]int64{"a": 1, "b": 2, "c": 3, "d": 4}},
		{"msgp", msgpUser{Name: "alice", Age: 30}},
	}
	for _, tc := range cases {
		encoded, err := Encode(Msgp, nil, tc.v)
		if err != nil {
			b.Fatalf("Encode(%s): %v", tc.name, err)
		}
		typ := reflect.TypeOf(tc.v)
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				dst := reflect.New(typ)
				if _, err := Unmarshal(Msgp, encoded, dst.Interface()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
