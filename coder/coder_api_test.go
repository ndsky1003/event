package coder

import (
	"strings"
	"testing"
)

// testCoder 是仅用于测试的 Coder 实现：Encode 总是追加一个字节 0xfe，Decode 直接返回 nil。
type testCoder struct{}

func (c *testCoder) Encode(b []byte, v any) ([]byte, error) { return append(b, 0xfe), nil }
func (c *testCoder) Decode(b []byte, v any) ([]byte, error) { return nil, nil }

func TestTString(t *testing.T) {
	cases := map[T]string{
		Raw:    "Raw",
		JSON:   "JSON",
		Msgp:   "Msgp",
		T(3):   "Unknown",
		T(9):   "Unknown",
		T(255): "Unknown",
	}
	for in, want := range cases {
		if got := in.String(); got != want {
			t.Errorf("T(%d).String() = %q, want %q", in, got, want)
		}
	}
}

// TestEncodeDecodeOutOfRange 覆盖 t >= Max 的越界分支，确认返回错误而非数组越界 panic。
func TestEncodeDecodeOutOfRange(t *testing.T) {
	for _, tt := range []T{Max, Max + 1, T(254), T(255)} {
		if _, err := Encode(tt, nil, "x"); err == nil {
			t.Errorf("Encode(%d) want error, got nil", tt)
		}
		if _, err := Decode(tt, []byte("x"), new(string)); err == nil {
			t.Errorf("Decode(%d) want error, got nil", tt)
		}
	}
}

// TestErrorContainsValue 确认错误信息用 %d 打印了具体数值，而不是被 String() 吞成 "Unknown"。
func TestErrorContainsValue(t *testing.T) {
	_, err := Encode(T(255), nil, "x")
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "255") {
		t.Errorf("error should contain numeric value, got %q", err.Error())
	}
}

// TestEncodeDecodeUnregistered 覆盖「已分配但未注册」的 T(3)..T(9) 分支。
func TestEncodeDecodeUnregistered(t *testing.T) {
	for _, tt := range []T{T(3), T(4), T(9)} {
		if _, err := Encode(tt, nil, "x"); err == nil {
			t.Errorf("Encode(%d) want error for unregistered coder, got nil", tt)
		}
		if _, err := Decode(tt, []byte("x"), new(string)); err == nil {
			t.Errorf("Decode(%d) want error for unregistered coder, got nil", tt)
		}
	}
}

func TestDecodeNilV(t *testing.T) {
	if _, err := Decode(Msgp, []byte("x"), nil); err == nil {
		t.Fatal("Decode with nil v want error, got nil")
	}
}

func TestRegisterCoder(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		t.Cleanup(func() { coders[T(3)] = nil })
		if err := RegisterCoder(T(3), &testCoder{}); err != nil {
			t.Fatal(err)
		}
		b, err := Encode(T(3), nil, "ignored")
		if err != nil {
			t.Fatal(err)
		}
		if len(b) != 1 || b[0] != 0xfe {
			t.Fatalf("Encode via registered coder = %v, want [0xfe]", b)
		}
	})

	t.Run("out_of_range", func(t *testing.T) {
		if err := RegisterCoder(Max, &testCoder{}); err == nil {
			t.Fatal("RegisterCoder(Max) want error, got nil")
		}
		if err := RegisterCoder(T(255), &testCoder{}); err == nil {
			t.Fatal("RegisterCoder(255) want error, got nil")
		}
	})

	t.Run("nil_interface", func(t *testing.T) {
		if err := RegisterCoder(T(3), nil); err == nil {
			t.Fatal("RegisterCoder with nil interface want error, got nil")
		}
	})

	t.Run("typed_nil", func(t *testing.T) {
		var c *testCoder
		if err := RegisterCoder(T(3), c); err == nil {
			t.Fatal("RegisterCoder with typed nil want error, got nil")
		}
	})

	t.Run("override_builtin", func(t *testing.T) {
		orig := coders[Raw]
		t.Cleanup(func() { coders[Raw] = orig })
		if err := RegisterCoder(Raw, &testCoder{}); err != nil {
			t.Fatal(err)
		}
		b, err := Encode(Raw, nil, []byte("x"))
		if err != nil {
			t.Fatal(err)
		}
		if len(b) != 1 || b[0] != 0xfe {
			t.Fatalf("Encode via overridden Raw = %v, want [0xfe]", b)
		}
	})
}

// TestEncodeNilValue 验证编码 nil 值不报错（msgpack 编码 nil），并能被解码回 nil。
func TestEncodeNilValue(t *testing.T) {
	b, err := Encode(Msgp, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var dst any
	if _, err := Decode(Msgp, b, &dst); err != nil {
		t.Fatal(err)
	}
	if dst != nil {
		t.Fatalf("want nil, got %v", dst)
	}
}
