package event

import (
	"bytes"
	"errors"
	"reflect"
	"sync"

	"github.com/ndsky1003/event/v2/msg"
	"github.com/tinylib/msgp/msgp"
	"github.com/vmihailenco/msgpack/v5"
)

// ============ 错误定义 ============
var (
	ErrNoConnect    = errors.New("ErrNoConnect")
	ErrClient       = errors.New("ErrClient")
	ErrClientEncode = errors.New("ErrClientEncode")
	ErrServer       = errors.New("ErrServer")
	ErrRemoteClient = errors.New("ErrRemoteClient")
)

var (
	sliceType = reflect.TypeFor[[]string]()
	errType   = reflect.TypeFor[error]()
	anyType   = reflect.TypeFor[any]()
)

// ============ Handler 接口 - 零反射路径 ============

// Handler 事件处理器接口
// 实现此接口可以获得零反射调用性能
type Handler interface {
	Handle(params ...any) error
}

// HandlerFunc 函数类型，实现 Handler 接口
type HandlerFunc func(params ...any) error

func (f HandlerFunc) Handle(params ...any) error {
	return f(params...)
}

// ============ Call ============
type Call struct {
	Msg  *msg.Msg
	Done chan *Call
	Err  error
}

func NewCall(m *msg.Msg) *Call {
	if m == nil {
		return nil
	}
	return &Call{
		Msg:  m,
		Done: make(chan *Call, 1),
	}
}

func (this *Call) done() {
	select {
	case this.Done <- this:
	default:
	}
}

// ============ method ============
type method struct {
	// 反射调用路径
	function  reflect.Value
	argsType  []*argType
	argsCount int

	// 快速调用路径 - Handler 接口
	handlerInterface any // Handler 接口实例
	isHandler       bool // 是否使用 Handler 接口

	// 缓存优化
	cachedArgs []reflect.Value // 预分配的参数
	cachedFunc any             // 缓存的函数值
}

type argType struct {
	isPointer bool
	at        reflect.Type
}

// ============ Pools ============
var (
	// Buffer pool
	bufPool = sync.Pool{
		New: func() any {
			return new(bytes.Buffer)
		},
	}

	// msgp.Writer pool
	writerPool = sync.Pool{
		New: func() any {
			return msgp.NewWriter(nil)
		},
	}

	// bytes.Reader pool
	readerPool = sync.Pool{
		New: func() any {
			return bytes.NewReader(nil)
		},
	}

	// msg.Msg pool
	msgPool = sync.Pool{
		New: func() any {
			return &msg.Msg{}
		},
	}
)

// ============ Codec ============
type msgpCoder struct{}

var coder = newMsgpCoder()

func newMsgpCoder() *msgpCoder {
	return &msgpCoder{}
}

func (c *msgpCoder) marshal(v any) ([]byte, error) {
	if v == nil {
		return []byte{0xC0}, nil
	}

	// 1. 优先尝试 Marshaler
	if value, ok := v.(msgp.Marshaler); ok {
		data, err := value.MarshalMsg(nil)
		if err != nil {
			return nil, err
		}
		return data, nil
	}

	var buf bytes.Buffer
	// 2. Encodable
	if value, ok := v.(msgp.Encodable); ok {
		w := writerPool.Get().(*msgp.Writer)
		w.Reset(&buf)
		err := value.EncodeMsg(w)
		if err == nil {
			err = w.Flush()
		}
		writerPool.Put(w)
		if err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}

	// 3. msgpack 反射
	enc := msgpack.GetEncoder()
	defer msgpack.PutEncoder(enc)
	enc.Reset(&buf)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (c *msgpCoder) unmarshal(data []byte, v any) error {
	if v == nil {
		return nil
	}

	// 1. Unmarshaler
	if value, ok := v.(msgp.Unmarshaler); ok {
		_, err := value.UnmarshalMsg(data)
		return err
	}

	// 2. nil 处理
	if msgp.IsNil(data) {
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Pointer {
			if rv.IsNil() {
				return nil
			}
			rv.Elem().Set(reflect.Zero(rv.Elem().Type()))
			return nil
		}
		return errors.New("cannot set nil to non-pointer")
	}

	// 3. Decodable
	if value, ok := v.(msgp.Decodable); ok {
		reader := readerPool.Get().(*bytes.Reader)
		reader.Reset(data)
		err := value.DecodeMsg(msgp.NewReader(reader))
		readerPool.Put(reader)
		return err
	}

	// 4. 反射兜底
	dec := msgpack.GetDecoder()
	defer msgpack.PutDecoder(dec)
	reader := readerPool.Get().(*bytes.Reader)
	reader.Reset(data)
	dec.Reset(reader)
	defer readerPool.Put(reader)
	return dec.Decode(v)
}

// encodeMsg 编码消息
func encodeMsg(m *msg.Msg) ([]byte, error) {
	return coder.marshal(m)
}

// decodeMsg 解码消息
func decodeMsg(data []byte) (*msg.Msg, error) {
	m := msgPool.Get().(*msg.Msg)
	*m = msg.Msg{}

	err := coder.unmarshal(data, m)
	if err != nil {
		msgPool.Put(m)
		return nil, err
	}
	return m, nil
}

// releaseMsg 释放消息到 pool
func releaseMsg(m *msg.Msg) {
	if m != nil {
		*m = msg.Msg{}
		msgPool.Put(m)
	}
}
