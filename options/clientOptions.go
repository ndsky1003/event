package options

import (
	"io"
	"time"

	"github.com/ndsky1003/event/codec"
)

type CreateClientCodecFunc func(conn io.ReadWriteCloser) (codec.Codec, error)
type ClientOptions struct {
	Name          *string
	CodecFunc     *CreateClientCodecFunc
	CheckInterval *time.Duration
	HeartInterval *time.Duration
	IsStopHeart   *bool
}

func Client() *ClientOptions {
	return new(ClientOptions)
}
func (this *ClientOptions) SetName(name string) *ClientOptions {
	if this == nil {
		return this
	}
	this.Name = &name
	return this
}

func (this *ClientOptions) SetCodecFunc(cf CreateClientCodecFunc) *ClientOptions {
	if this == nil {
		return this
	}
	this.CodecFunc = &cf
	return this
}
func (this *ClientOptions) SetCheckInterval(t time.Duration) *ClientOptions {
	if this == nil {
		return this
	}
	this.CheckInterval = &t
	return this
}
func (this *ClientOptions) SetHeartInterval(t time.Duration) *ClientOptions {
	if this == nil {
		return this
	}
	this.HeartInterval = &t
	return this
}

func (this *ClientOptions) SetIsStopHeart(is bool) *ClientOptions {
	if this == nil {
		return this
	}
	this.IsStopHeart = &is
	return this
}

func (this *ClientOptions) Merge(opts ...*ClientOptions) *ClientOptions {
	for _, opt := range opts {
		this.merge(opt)
	}
	return this
}

func (this *ClientOptions) merge(opt *ClientOptions) {
	if opt.Name != nil {
		this.Name = opt.Name
	}
	if opt.CodecFunc != nil {
		this.CodecFunc = opt.CodecFunc
	}
	if opt.CheckInterval != nil {
		this.CheckInterval = opt.CheckInterval
	}
	if opt.HeartInterval != nil {
		this.HeartInterval = opt.HeartInterval
	}
	if opt.IsStopHeart != nil {
		this.IsStopHeart = opt.IsStopHeart
	}
}
