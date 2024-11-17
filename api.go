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
	return this.emit(msgtype.ReqSomeOne, en, args...)
}

func (this *Client) EmitSomeOneAsnyc(en eventname.T, args ...any) *Call {
	return this.emit_async(msgtype.ReqSomeOne, en, args...)
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
		argsType = append(argsType, &argType{isPointer: at.Kind() == reflect.Pointer, at: at})
	}
	ft := reflect.ValueOf(Func)
	mType := &method{
		function:  ft,
		argsType:  argsType,
		argsCount: inCount,
	}
	this.Lock()
	var isExist bool
	var methods []*method
	var topic *topic.Topic
	for tp, ev := range this.topics {
		if tp.Equal(t) {
			methods = append(ev, mType)
			topic = tp
			isExist = true
			break
		}
	}
	if isExist {
		this.topics[topic] = methods
	} else {
		methods = []*method{mType}
		this.topics[newtp] = methods
	}
	this.Unlock()
	if !isExist {
		go this.regist_topic()
	}
}
