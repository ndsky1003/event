package options

import (
	"io"
	"testing"

	"github.com/ndsky1003/event/codec"
)

func TestClientOptionsSetCodecFunc(t *testing.T) {
	var called bool
	cf := func(conn io.ReadWriteCloser) (codec.Codec, error) {
		called = true
		return nil, nil
	}
	opt := Client().SetCodecFunc(cf)
	if opt.CodecFunc == nil {
		t.Fatal("SetCodecFunc 应设置 CodecFunc")
	}
	c, err := (*opt.CodecFunc)(nil)
	if c != nil || err != nil {
		t.Errorf("codec func 返回 %v,%v,期望 nil,nil", c, err)
	}
	if !called {
		t.Error("codec func 应被调用")
	}
}

func TestServerOptionsSetCodecFunc(t *testing.T) {
	var called bool
	cf := func(conn io.ReadWriteCloser) (codec.Codec, error) {
		called = true
		return nil, nil
	}
	opt := Server().SetCodecFunc(cf)
	if opt.CodecFunc == nil {
		t.Fatal("SetCodecFunc 应设置 CodecFunc")
	}
	c, err := (*opt.CodecFunc)(nil)
	if c != nil || err != nil {
		t.Errorf("codec func 返回 %v,%v,期望 nil,nil", c, err)
	}
	if !called {
		t.Error("codec func 应被调用")
	}
}
