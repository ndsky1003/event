package event

import (
	"time"
)

type ClientOption struct {
	name           *string
	secret         *string
	check_interval *time.Duration //链接检测,不能说断链了就马上去连,这会造成不必要的资源消耗
	heart_interval *time.Duration //心跳检测的间隔
	is_wrap_error  *bool
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

func (this *ClientOption) SetCheckInterval(t time.Duration) *ClientOption {
	if this == nil {
		return this
	}
	this.check_interval = &t
	return this
}
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
}

type ServerOption struct {
	Timeout       *time.Duration
	secret        *string
	is_wrap_error *bool //默认情况下会包装,这个错误来自哪
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
	if opt.secret != nil {
		this.secret = opt.secret
	}
	if opt.is_wrap_error != nil {
		this.is_wrap_error = opt.is_wrap_error
	}
}

func (this *ServerOption) SetTimeout(t time.Duration) *ServerOption {
	if this == nil {
		return this
	}
	this.Timeout = &t
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
