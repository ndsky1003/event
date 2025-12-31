package event

import (
	"reflect"

	"github.com/ndsky1003/event/v2/eventname"
	"github.com/ndsky1003/event/v2/msgtype"
	"github.com/ndsky1003/event/v2/topic"
)

// ============ Emit 方法 ============

// EmitOne 随机发送给一个监听者
func (this *Client) EmitOne(en eventname.T, args ...any) error {
	return this.emit(msgtype.ReqOne, en, args...)
}

// EmitOneAsync 随机发送给一个监听者（异步）
func (this *Client) EmitOneAsync(en eventname.T, args ...any) *Call {
	return this.emitAsync(msgtype.ReqOne, en, args...)
}

// EmitAll 发送给所有监听者，等待所有响应，收集所有错误
func (this *Client) EmitAll(en eventname.T, args ...any) error {
	return this.emit(msgtype.ReqAll, en, args...)
}

// EmitAllAsync 发送给所有监听者（异步）
func (this *Client) EmitAllAsync(en eventname.T, args ...any) *Call {
	return this.emitAsync(msgtype.ReqAll, en, args...)
}

// EmitFirst 发送给所有监听者，只接受第一个返回值
func (this *Client) EmitFirst(en eventname.T, args ...any) error {
	return this.emit(msgtype.ReqFirst, en, args...)
}

// EmitFirstAsync 发送给所有监听者（异步）
func (this *Client) EmitFirstAsync(en eventname.T, args ...any) *Call {
	return this.emitAsync(msgtype.ReqFirst, en, args...)
}

// ============ On 方法 - 自动优化路径 ============

// On 监听事件
// 如果是正则匹配，第一个参数必须是 []string (正则子匹配)
// 性能优化：func(...any) error 签名的函数会自动走零反射路径
func (this *Client) On(t eventname.T, fn any) {
	if t == "" {
		panic("eventname must not empty")
	}

	// 尝试转换为 Handler 接口以获得零反射调用
	if handler, ok := this.tryWrapAsHandler(t, fn); ok {
		this.addHandlerInternal(t, handler)
		return
	}

	// 回退到反射路径
	this.addReflectHandler(t, fn)
}

// tryWrapAsHandler 尝试将函数包装成 Handler 接口
func (this *Client) tryWrapAsHandler(t eventname.T, fn any) (Handler, bool) {
	rv := reflect.ValueOf(fn)
	if rv.Kind() != reflect.Func {
		return nil, false
	}

	rt := rv.Type()
	if rt.NumOut() != 1 || rt.Out(0) != errType {
		return nil, false
	}

	// 检查是否是 func(...any) error 签名
	if this.isVariadicAny(rt) {
		// 快速路径：零反射调用
		return HandlerFunc(func(params ...any) error {
			out := rv.Call(paramsToReflectValues(params))
			if len(out) > 0 && out[0].IsValid() && !out[0].IsNil() {
				return out[0].Interface().(error)
			}
			return nil
		}), true
	}

	return nil, false
}

func (this *Client) isVariadicAny(rt reflect.Type) bool {
	if !rt.IsVariadic() {
		return false
	}
	// 最后一个参数是 ...any
	return rt.In(rt.NumIn()-1) == anyType
}

func paramsToReflectValues(params []any) []reflect.Value {
	if len(params) == 0 {
		return nil
	}
	result := make([]reflect.Value, len(params))
	for i, p := range params {
		result[i] = reflect.ValueOf(p)
	}
	return result
}

// addHandlerInternal 添加 Handler（零反射路径）
func (this *Client) addHandlerInternal(t eventname.T, handler Handler) {
	newtp := topic.New(t)
	mType := &method{
		handlerInterface: handler,
		isHandler:       true,
	}

	this.rwl.Lock()
	var isExist bool
	var methods []*method
	var existTp *topic.Topic
	for tp, ev := range this.topics {
		if tp.Equal(t) {
			methods = append(ev, mType)
			existTp = tp
			isExist = true
			break
		}
	}
	if isExist {
		this.topics[existTp] = methods
	} else {
		methods = []*method{mType}
		this.topics[newtp] = methods
	}
	this.rwl.Unlock()
	if !isExist {
		go this.registTopic()
	}
}

// addReflectHandler 添加反射处理器（兼容路径）
func (this *Client) addReflectHandler(t eventname.T, fn any) {
	rt := reflect.TypeOf(fn)
	if rt.Kind() != reflect.Func {
		panic("on a not func")
	}
	if rt.NumOut() != 1 {
		panic("on regist func must has 1 return value")
	}
	if rt.Out(0) != errType {
		panic("on regist func must has 1 return value of error")
	}

	inCount := rt.NumIn()
	length := inCount
	newtp := topic.New(t)
	var inStart int
	if newtp.IsReg {
		if inCount < 1 {
			panic("regist reg Func, first param must exist")
		}
		if rt.In(0) != sliceType {
			panic("regist reg Func, first param must []string type")
		}
		inStart = 1
		inCount = inCount - 1
	}

	var argsType = make([]*argType, 0, inCount)
	for i := inStart; i < length; i++ {
		at := rt.In(i)
		argsType = append(argsType, &argType{isPointer: at.Kind() == reflect.Pointer, at: at})
	}

	mType := &method{
		function:  reflect.ValueOf(fn),
		argsType:  argsType,
		argsCount: inCount,
		isHandler: false,
	}

	this.rwl.Lock()
	var isExist bool
	var methods []*method
	var existTp *topic.Topic
	for tp, ev := range this.topics {
		if tp.Equal(t) {
			methods = append(ev, mType)
			existTp = tp
			isExist = true
			break
		}
	}
	if isExist {
		this.topics[existTp] = methods
	} else {
		methods = []*method{mType}
		this.topics[newtp] = methods
	}
	this.rwl.Unlock()
	if !isExist {
		go this.registTopic()
	}
}

// ============ 便捷方法 - 使用优化路径 ============

// OnFunc 注册 func(...any) error 形式的函数（零反射）
func (this *Client) OnFunc(t eventname.T, fn func(...any) error) {
	this.On(t, fn) // 会自动走优化路径
}

// OnHandler 注册 Handler 接口（零反射）
func (this *Client) OnHandler(t eventname.T, handler Handler) {
	if t == "" {
		panic("eventname must not empty")
	}
	this.addHandlerInternal(t, handler)
}
