package options

import (
	"io"
	"time"

	"github.com/ndsky1003/event/codec"
)

type CreateServerCodecFunc func(conn io.ReadWriteCloser) (codec.Codec, error)

type ServerOptions struct {
	CodecFunc  *CreateServerCodecFunc
	ReqTimeout *time.Duration
}

func Server() *ServerOptions {
	return new(ServerOptions)
}

func (this *ServerOptions) Merge(opts ...*ServerOptions) *ServerOptions {
	for _, opt := range opts {
		this.merge(opt)
	}
	return this
}

func (this *ServerOptions) merge(opt *ServerOptions) {
	if opt.CodecFunc != nil {
		this.CodecFunc = opt.CodecFunc
	}
	if opt.ReqTimeout != nil {
		this.ReqTimeout = opt.ReqTimeout
	}
}

func (this *ServerOptions) SetCodecFunc(cf CreateServerCodecFunc) *ServerOptions {
	this.CodecFunc = &cf
	return this
}
func (this *ServerOptions) SetReqTimeout(t time.Duration) *ServerOptions {
	this.ReqTimeout = &t
	return this
}
