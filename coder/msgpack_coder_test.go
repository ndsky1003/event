package coder

import (
	"bytes"
	"math"
	"strings"
	"testing"
	"time"
)

// ---- 补充测试用类型 ----

type tagged struct {
	Name string `msgpack:"n"`
	Age  int32  `msgpack:"a"`
}

type base struct {
	ID int64
}

type embedded struct {
	base
	Extra string
}

type withPtrField struct {
	P *plain
	S []byte
}

type withIface struct {
	V any
}

// ---- 标量类型全覆盖 ----

func TestMsgpackScalarTypes(t *testing.T) {
	cases := []struct {
		name string
		v    any
	}{
		{"bool_true", true},
		{"bool_false", false},

		{"int", int(-12345)},
		{"int_zero", int(0)},
		{"int8_min", int8(-128)},
		{"int8_max", int8(127)},
		{"int16_min", int16(-32768)},
		{"int16_max", int16(32767)},
		{"int32_min", int32(-2147483648)},
		{"int32_max", int32(2147483647)},
		{"int64_min", int64(-9223372036854775808)},
		{"int64_max", int64(9223372036854775807)},

		{"uint", uint(12345)},
		{"uint8_max", uint8(255)},
		{"uint16_max", uint16(65535)},
		{"uint32_max", uint32(4294967295)},
		{"uint64_max", uint64(18446744073709551615)},

		{"float32", float32(1.5)},
		{"float64", float64(3.141592653589793)},

		{"string", "hello, 世界 🌍"},
		{"empty_string", ""},
		{"long_string", strings.Repeat("a", 70000)},

		{"bytes", []byte{0x00, 0x01, 0xff}},
		{"empty_bytes", []byte{}},
		{"long_bytes", bytes.Repeat([]byte{0xab}, 70000)},

		{"rune", '中'},
		{"byte", byte('A')},

		{"duration", time.Duration(123456789)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRoundtrip(t, Msgp, tc.v)
		})
	}
}

// ---- 复合类型全覆盖 ----

func TestMsgpackCompositeTypes(t *testing.T) {
	p := &plain{Name: "p", N: 9}
	pp := &p
	cases := []struct {
		name string
		v    any
	}{
		{"slice_int", []int{1, 2, 3}},
		{"slice_string", []string{"a", "b", "c"}},
		{"slice_struct", []plain{{Name: "x"}, {Name: "y"}}},
		{"slice_2d", [][]int{{1, 2}, {3, 4}}},
		{"empty_slice", []int{}},
		{"nil_slice", []int(nil)},

		{"array", [3]int{1, 2, 3}},
		{"array_struct", [2]plain{{Name: "a"}, {Name: "b"}}},

		{"map_str_str", map[string]string{"a": "1", "b": "2"}},
		{"map_str_int64", map[string]int64{"a": 1, "b": -2}},
		{"map_int_str", map[int]string{1: "a", 2: "b"}},
		{"map_str_slice", map[string][]int{"a": {1, 2}, "b": {3}}},
		{"map_str_map", map[string]map[string]int{"a": {"b": 1}}},
		{"map_str_any", map[string]any{"s": "x", "b": true, "f": 1.5}},
		{"empty_map", map[string]int{}},
		{"nil_map", map[string]int(nil)},

		{"struct", plain{Name: "x", N: 7, Tags: []string{"a"}, Meta: map[string]int64{"k": 1}}},
		{"nested_struct", outer{Inner: inner{A: 1, B: "b"}, Flag: true}},
		{"tagged", tagged{Name: "n", Age: 30}},
		{"embedded", embedded{base: base{ID: 9}, Extra: "e"}},
		{"ptr_field", withPtrField{P: &plain{Name: "p", N: 1}, S: []byte{1, 2}}},
		{"ptr_field_nil", withPtrField{P: nil, S: nil}},
		{"iface_string", withIface{V: "s"}},
		{"iface_bool", withIface{V: true}},

		{"ptr", &plain{Name: "p", N: 9}},
		{"double_ptr", pp},
		{"ptr_nil", (*plain)(nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRoundtrip(t, Msgp, tc.v)
		})
	}
}

// ---- msgp 快路径类型嵌套在复合容器里（走反射，验证互通） ----

func TestMsgpackMsgpNested(t *testing.T) {
	cases := []struct {
		name string
		v    any
	}{
		{"msgp_in_slice", []msgpUser{{Name: "a", Age: 1}, {Name: "b", Age: 2}}},
		{"msgp_in_map", map[string]msgpUser{"x": {Name: "a", Age: 1}}},
		{"msgp_in_struct", struct{ M msgpUser }{M: msgpUser{Name: "a", Age: 1}}},
		{"msgp_ptr_in_slice", []*msgpUser{{Name: "a", Age: 1}, {Name: "b", Age: 2}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRoundtrip(t, Msgp, tc.v)
		})
	}
}

// ---- 浮点特殊值（NaN/Inf 不能用 DeepEqual 比较） ----

func TestMsgpackFloatSpecial(t *testing.T) {
	cases := []struct {
		name  string
		v     float64
		check func(float64) bool
	}{
		{"nan", math.NaN(), math.IsNaN},
		{"pos_inf", math.Inf(1), func(f float64) bool { return math.IsInf(f, 1) }},
		{"neg_inf", math.Inf(-1), func(f float64) bool { return math.IsInf(f, -1) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := Encode(Msgp, nil, tc.v)
			if err != nil {
				t.Fatalf("Encode(%s): %v", tc.name, err)
			}
			var got float64
			rest, err := Decode(Msgp, b, &got)
			if err != nil {
				t.Fatalf("Decode(%s): %v", tc.name, err)
			}
			if len(rest) != 0 {
				t.Fatalf("Decode(%s) 剩余 %d 字节", tc.name, len(rest))
			}
			if !tc.check(got) {
				t.Fatalf("%s: got %v, check failed", tc.name, got)
			}
		})
	}
}

// ---- time.Time（时区可能变化，用 Equal 比较） ----

func TestMsgpackTime(t *testing.T) {
	cases := []time.Time{
		time.Date(2026, 11, 19, 12, 30, 0, 123456789, time.UTC),
		time.Date(2026, 11, 19, 12, 30, 0, 0, time.FixedZone("CST", 8*3600)),
		time.Time{}, // 零值
	}
	for i, in := range cases {
		b, err := Encode(Msgp, nil, in)
		if err != nil {
			t.Fatalf("Encode time[%d]: %v", i, err)
		}
		var got time.Time
		rest, err := Decode(Msgp, b, &got)
		if err != nil {
			t.Fatalf("Decode time[%d]: %v", i, err)
		}
		if len(rest) != 0 {
			t.Fatalf("Decode time[%d] 剩余 %d 字节", i, len(rest))
		}
		if !got.Equal(in) {
			t.Fatalf("time[%d] 往返不一致: got %v, want %v", i, got, in)
		}
	}
}

// ---- 不支持的类型应返回错误而非 panic ----

func TestMsgpackUnsupported(t *testing.T) {
	for _, v := range []any{complex64(1 + 2i), complex128(3 + 4i)} {
		if _, err := Encode(Msgp, nil, v); err == nil {
			t.Errorf("Encode(%T) want error, got nil", v)
		}
	}
}
