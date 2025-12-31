package event

import "time"

// ClientOption 客户端选项
type ClientOption struct {
	Name          *string
	Secret        *string
	IsWrapError   *bool
}

// ClientOptions 创建客户端选项
func ClientOptions() *ClientOption {
	return new(ClientOption)
}

func (o *ClientOption) SetName(name string) *ClientOption {
	if o == nil {
		return o
	}
	o.Name = &name
	return o
}

func (o *ClientOption) SetSecret(secret string) *ClientOption {
	if o == nil {
		return o
	}
	o.Secret = &secret
	return o
}

func (o *ClientOption) SetIsWrapError(b bool) *ClientOption {
	if o == nil {
		return o
	}
	o.IsWrapError = &b
	return o
}

func (o *ClientOption) merges(opts ...*ClientOption) *ClientOption {
	for _, opt := range opts {
		o.merge(opt)
	}
	return o
}

// Merge 导出的合并方法
func (o *ClientOption) Merge(opts ...*ClientOption) *ClientOption {
	return o.merges(opts...)
}

func (o *ClientOption) merge(opt *ClientOption) {
	if opt == nil {
		return
	}
	if opt.Name != nil {
		o.Name = opt.Name
	}
	if opt.Secret != nil {
		o.Secret = opt.Secret
	}
	if opt.IsWrapError != nil {
		o.IsWrapError = opt.IsWrapError
	}
}

// ServerOption 服务端选项
type ServerOption struct {
	Timeout     *time.Duration
	Secret      *string
	IsWrapError *bool
}

// ServerOptions 创建服务端选项
func ServerOptions() *ServerOption {
	return new(ServerOption)
}

func (o *ServerOption) merges(opts ...*ServerOption) *ServerOption {
	for _, opt := range opts {
		o.merge(opt)
	}
	return o
}

// Merge 导出的合并方法
func (o *ServerOption) Merge(opts ...*ServerOption) *ServerOption {
	return o.merges(opts...)
}

func (o *ServerOption) merge(opt *ServerOption) {
	if opt == nil {
		return
	}
	if opt.Timeout != nil {
		o.Timeout = opt.Timeout
	}
	if opt.Secret != nil {
		o.Secret = opt.Secret
	}
	if opt.IsWrapError != nil {
		o.IsWrapError = opt.IsWrapError
	}
}

func (o *ServerOption) SetTimeout(t time.Duration) *ServerOption {
	if o == nil {
		return o
	}
	o.Timeout = &t
	return o
}

func (o *ServerOption) SetSecret(secret string) *ServerOption {
	if o == nil {
		return o
	}
	o.Secret = &secret
	return o
}

func (o *ServerOption) SetIsWrapError(b bool) *ServerOption {
	if o == nil {
		return o
	}
	o.IsWrapError = &b
	return o
}
