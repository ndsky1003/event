package event

import (
	"reflect"

	"github.com/ndsky1003/event/v2/eventname"
	"github.com/ndsky1003/event/v2/msgtype"
	"github.com/ndsky1003/event/v2/topic"
)

func (this *Client) Emit(en eventname.T, args ...any) error {
	return this.emit(msgtype.Req, en, args...)
}

func (this *Client) EmitAsync(en eventname.T, args ...any) (call *Call) {
	return this.emit_async(msgtype.Req, en, args...)
}

func (this *Client) EmitSomeOne(en eventname.T, args ...any) error {
	return this.emit(msgtype.ReqRandOne, en, args...)
}

func (this *Client) EmitSomeOneAsnyc(en eventname.T, args ...any) *Call {
	return this.emit_async(msgtype.ReqRandOne, en, args...)
}

// 监听事件
// if t is reg , first param must the value of reg.FindStringSubmatch(et)
func (this *Client) On(t eventname.T, Func any) {
	if t == "" {
		panic("eventname must not empty")
	}
	rt := reflect.TypeOf(Func)
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
		argsType = append(argsType, &argType{at: at})
	}
	ft := reflect.ValueOf(Func)
	mType := &method{
		function:  ft,
		argsType:  argsType,
		argsCount: inCount,
	}
	this.rwl.Lock()
	var isExist bool
	var methods []*method
	var existTP *topic.Topic
	for tp, ev := range this.topics {
		if tp.Equal(t) {
			methods = append(ev, mType)
			existTP = tp
			isExist = true
			break
		}
	}
	if isExist {
		this.topics[existTP] = methods
	} else {
		methods = []*method{mType}
		this.topics[newtp] = methods
	}
	this.rwl.Unlock()
	if !isExist {
		// 同步注册。用 go 的话，On 之后紧跟的 Emit 可能先到达服务端——
		// 那时 topic 还没注册，事件直接变成"无人监听"被丢掉，而且不报错。
		// 服务端现在也是同步处理单条连接的消息（service.HandleMsg），
		// 两端一起才真正保证顺序。
		// 连接还没起来时 regist_topic 会立刻返回，connect 成功后再补注册，
		// 所以这里不会阻塞在"没连接"上。
		this.regist_topic()
	}
}
