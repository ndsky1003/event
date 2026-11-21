package event

import (
	"time"

	"github.com/ndsky1003/event/v3/coder"
)

type ClientOption struct {
	name           *string
	listen_addrs   []string //节点自己的监听地址列表（可变），供其他节点连接。支持 unix:// 前缀和多个 ip:端口。
	secret         *string
	check_interval *time.Duration //链接检测,不能说断链了就马上去连,这会造成不必要的资源消耗。单位就是 time.Duration 本身,不要再乘 time.Second
	is_wrap_error  *bool
	coder_type     *coder.T //事件参数的编码方式,默认为 Msgp
	connOption
	timeout *time.Duration //等待所有监听者响应的超时。单位就是 time.Duration 本身,不要再乘 time.Second
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

// SetListenAddr 设置节点自己的监听地址（可变参数），供其他节点连接。
// 支持 unix:// 前缀和多个 ip:端口，如 SetListenAddr(":9000", "unix:///tmp/n.sock")。
// 不传参数时用默认值 "127.0.0.1:0"（系统分配随机空闲端口）。
// 传 ":0" 也会回填实际分配到的端口注册到目录。
func (this *ClientOption) SetListenAddr(addr ...string) *ClientOption {
	if this == nil {
		return this
	}
	this.listen_addrs = addr
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
	this.HeartInterval = &t
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

// SetTimeout 等待所有监听者响应的超时。t 是完整的 time.Duration，例如 10*time.Second，不是秒数。
func (this *ClientOption) SetTimeout(t time.Duration) *ClientOption {
	if this == nil {
		return this
	}
	this.timeout = &t
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
	this.connOption.merge(&opt.connOption)
	if opt.name != nil {
		this.name = opt.name
	}

	if opt.listen_addrs != nil {
		this.listen_addrs = opt.listen_addrs
	}

	if opt.check_interval != nil {
		this.check_interval = opt.check_interval
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
	if opt.timeout != nil {
		this.timeout = opt.timeout
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

type SendOption struct {
	timeout *time.Duration
	ct      *coder.T
}

func SendOptions() *SendOption {
	return &SendOption{}
}

// SetTimeout 设置本次发送的等待超时。t 是完整的 time.Duration，例如 10*time.Second，不是秒数。
func (this *SendOption) SetTimeout(t time.Duration) *SendOption {
	if this == nil {
		return this
	}
	this.timeout = &t
	return this
}

// SetCoderType 设置本次发送的事件参数编码方式（Raw/JSON/Msgp），默认跟随客户端。
func (this *SendOption) SetCoderType(t coder.T) *SendOption {
	if this == nil {
		return this
	}
	this.ct = &t
	return this
}

func (this *SendOption) merges(opts ...*SendOption) *SendOption {
	for _, opt := range opts {
		this.merge(opt)
	}
	return this
}

func (this *SendOption) merge(opt *SendOption) {
	if opt == nil {
		return
	}
	if opt.timeout != nil {
		this.timeout = opt.timeout
	}
	if opt.ct != nil {
		this.ct = opt.ct
	}
}

func (this *SendOption) mergeClientOption(opt *ClientOption) {
	if this.timeout == nil && opt.timeout != nil {
		this.timeout = opt.timeout
	}
	if this.ct == nil && opt.coder_type != nil {
		this.ct = opt.coder_type
	}
}
