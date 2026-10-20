package event

import (
	"time"

	"github.com/ndsky1003/event/v2/coder"
)

type ClientOption struct {
	name           *string
	secret         *string
	check_interval *time.Duration //链接检测,不能说断链了就马上去连,这会造成不必要的资源消耗。单位就是 time.Duration 本身,不要再乘 time.Second
	heart_interval *time.Duration //心跳检测的间隔。单位就是 time.Duration 本身
	is_wrap_error  *bool
	coder_type     *coder.T //事件参数的编码方式,默认为 Msgp
}

func ClientOptions() *ClientOption {
	return new(ClientOption)
}

func (this *ClientOption) SetName(name string) *ClientOption {
	if this == nil {
		return this
	}
	this.name = &name
	return this
}

func (this *ClientOption) SetSecret(secret string) *ClientOption {
	if this == nil {
		return this
	}
	this.secret = &secret
	return this
}

// SetCheckInterval 断线重连的检测间隔。t 是完整的 time.Duration，例如 2*time.Second，不是秒数。
func (this *ClientOption) SetCheckInterval(t time.Duration) *ClientOption {
	if this == nil {
		return this
	}
	this.check_interval = &t
	return this
}

// SetHeartInterval 心跳间隔。t 是完整的 time.Duration，例如 20*time.Second，不是秒数。
func (this *ClientOption) SetHeartInterval(t time.Duration) *ClientOption {
	if this == nil {
		return this
	}
	this.heart_interval = &t
	return this
}

func (this *ClientOption) SetIsWrapError(b bool) *ClientOption {
	if this == nil {
		return this
	}
	this.is_wrap_error = &b
	return this
}

// SetCoderType 设置事件参数的编码方式（Raw/JSON/Msgp），默认为 Msgp。
func (this *ClientOption) SetCoderType(t coder.T) *ClientOption {
	if this == nil {
		return this
	}
	this.coder_type = &t
	return this
}

func (this *ClientOption) merges(opts ...*ClientOption) *ClientOption {
	for _, opt := range opts {
		this.merge(opt)
	}
	return this
}

func (this *ClientOption) merge(opt *ClientOption) {
	if opt == nil {
		return
	}
	if opt.name != nil {
		this.name = opt.name
	}

	if opt.check_interval != nil {
		this.check_interval = opt.check_interval
	}

	if opt.heart_interval != nil {
		this.heart_interval = opt.heart_interval
	}

	if opt.secret != nil {
		this.secret = opt.secret
	}

	if opt.is_wrap_error != nil {
		this.is_wrap_error = opt.is_wrap_error
	}

	if opt.coder_type != nil {
		this.coder_type = opt.coder_type
	}
}

type ServerOption struct {
	Timeout        *time.Duration //等待所有监听者响应的超时。单位就是 time.Duration 本身,不要再乘 time.Second
	heart_interval *time.Duration //传输层心跳间隔。单位就是 time.Duration 本身
	secret         *string
	is_wrap_error  *bool //默认情况下会包装,这个错误来自哪
}

func ServerOptions() *ServerOption {
	return new(ServerOption)
}

func (this *ServerOption) merges(opts ...*ServerOption) *ServerOption {
	for _, opt := range opts {
		this.merge(opt)
	}
	return this
}

func (this *ServerOption) merge(opt *ServerOption) {
	if opt == nil {
		return
	}
	if opt.Timeout != nil {
		this.Timeout = opt.Timeout
	}
	if opt.heart_interval != nil {
		this.heart_interval = opt.heart_interval
	}
	if opt.secret != nil {
		this.secret = opt.secret
	}
	if opt.is_wrap_error != nil {
		this.is_wrap_error = opt.is_wrap_error
	}
}

// SetTimeout 等待所有监听者响应的超时。t 是完整的 time.Duration，例如 10*time.Second，不是秒数。
func (this *ServerOption) SetTimeout(t time.Duration) *ServerOption {
	if this == nil {
		return this
	}
	this.Timeout = &t
	return this
}

// SetHeartInterval 传输层心跳间隔。t 是完整的 time.Duration，例如 20*time.Second，不是秒数。
func (this *ServerOption) SetHeartInterval(t time.Duration) *ServerOption {
	if this == nil {
		return this
	}
	this.heart_interval = &t
	return this
}

func (this *ServerOption) SetSecret(secret string) *ServerOption {
	if this == nil {
		return this
	}
	this.secret = &secret
	return this
}

func (this *ServerOption) SetIsWrapError(b bool) *ServerOption {
	if this == nil {
		return this
	}
	this.is_wrap_error = &b
	return this
}
