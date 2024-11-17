package event

import (
	"errors"
	"reflect"
)

var (
	ErrNoConnect      = errors.New("ErrNoConnect")
	ErrClient         = errors.New("ErrClient")
	ErrClientEncode   = errors.New("ErrClientEncode")
	ErrClientDecode   = errors.New("ErrClientDecode")
	ErrClientCodecNil = errors.New("ErrClientCodec is nil")
	ErrServer         = errors.New("ErrServer")
	ErrRemoteClient   = errors.New("ErrRemoteClient")
)

// var errType = reflect.TypeOf((*error)(nil)).Elem()
// var sliceType = reflect.TypeOf((*[]string)(nil)).Elem()
var sliceType = reflect.TypeFor[[]string]()
var errType = reflect.TypeFor[error]()
