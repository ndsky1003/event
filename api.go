package event

import (
	"errors"
	"reflect"

	"github.com/ndsky1003/event/v3/eventname"
	"github.com/ndsky1003/event/v3/msgtype"
	"github.com/ndsky1003/event/v3/topic"
)

func (this *Client) Emit(en eventname.T, args ...any) error {
	call := <-this.EmitAsync(en, args...).Done
	return call.Err
}

func (this *Client) EmitAsync(en eventname.T, args ...any) (call *Call) {
	opt := SendOptions()
	opt.mergeClientOption(this.opt)
	return this.emit_async(msgtype.Req, en, *opt, args...)
}

func (this *Client) EmitSomeOne(en eventname.T, args ...any) error {
	call := <-this.EmitSomeOneAsnyc(en, args...).Done
	return call.Err
}

func (this *Client) EmitSomeOneAsnyc(en eventname.T, args ...any) *Call {
	opt := SendOptions()
	opt.mergeClientOption(this.opt)
	return this.emit_async(msgtype.ReqRandOne, en, *opt, args...)
}

// EmitBatch 批量发布：并发发送同一事件的多个参数组合，等所有监听者都响应后返回。
// 语义等同"并发调 N 次 EmitAsync 再等全部"，区别是这些消息会一起进发送队列，
// writePump 能攒批（writev 次数少、QPS 高）。返回与 argsList 一一对应的错误。
func (this *Client) EmitBatch(en eventname.T, argsList ...[]any) []error {
	if len(argsList) == 0 {
		return nil
	}
	calls := make([]*Call, 0, len(argsList))
	for _, args := range argsList {
		calls = append(calls, this.EmitAsync(en, args...))
	}
	errs := make([]error, len(calls))
	for i, call := range calls {
		<-call.Done
		errs[i] = call.Err
	}
	return errs
}

func (this *Client) Builder() *builder {
	return &builder{client: this}
}

// 监听事件
func (this *Client) On(t eventname.T, Func any) error {
	if t == "" {
		return errors.New("eventname must not empty")
	}
	rt := reflect.TypeOf(Func)
	if rt.Kind() != reflect.Func {
		return errors.New("on a not func")
	}
	if rt.NumOut() != 1 {
		return errors.New("on regist func must has 1 return value")
	}
	if rt.Out(0) != errType {
		return errors.New("on regist func must has 1 return value of error")
	}
	rv := reflect.ValueOf(Func)
	if !rv.IsValid() {
		return errors.New("regist func is nil")
	}
	newtp, err := topic.New(t)
	if err != nil {
		return err
	}
	argsType := make([]*argType, 0, rt.NumIn())
	for i := 0; i < rt.NumIn(); i++ {
		argsType = append(argsType, &argType{at: rt.In(i)})
	}
	mType := &method{
		function:  rv,
		argsType:  argsType,
		argsCount: rt.NumIn(),
	}
	this.rwl.Lock()
	var isExist bool
	var methods []*method
	var existIdx int
	for i := range this.topics {
		if this.topics[i].tp.Equal(t) {
			methods = append(this.topics[i].methods, mType)
			existIdx = i
			isExist = true
			break
		}
	}
	if isExist {
		this.topics[existIdx].methods = methods
	} else {
		methods = []*method{mType}
		this.topics = append(this.topics, topicEntry{tp: newtp, methods: methods})
	}
	this.rwl.Unlock()
	if !isExist {
		// 广播订阅：告诉所有 peer"我订阅了这个 topic"。
		// peer 订阅表（subs）据此把后续的 Emit 点对点路由过来。
		this.broadcastSubscribe(t)
	}
	return nil
}
