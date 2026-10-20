package event

import (
	"errors"
	"reflect"
)

var (
	ErrNoConnect    = errors.New("ErrNoConnect")
	ErrClient       = errors.New("ErrClient")
	ErrClientEncode = errors.New("ErrClientEncode")
	ErrClientDecode = errors.New("ErrClientDecode")
	ErrServer       = errors.New("ErrServer")
	ErrRemoteClient = errors.New("ErrRemoteClient")
	// ErrConnClosed 连接已关闭（主动 Close 或 pump 退出后）。用 errors.Is 判断，
	// 用它区分"正常收尾"和"异常断连"。
	ErrConnClosed = errors.New("ErrConnClosed")
)

// var errType = reflect.TypeOf((*error)(nil)).Elem()
// var sliceType = reflect.TypeOf((*[]string)(nil)).Elem()
var sliceType = reflect.TypeFor[[]string]()
var errType = reflect.TypeFor[error]()
