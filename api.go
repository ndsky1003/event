package event

import (
	"errors"
	"reflect"

	"github.com/ndsky1003/event/v2/eventname"
	"github.com/ndsky1003/event/v2/msgtype"
	"github.com/ndsky1003/event/v2/topic"
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
// if t is reg , first param must the value of reg.FindStringSubmatch(et)
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
	inCount := rt.NumIn()
	length := inCount
	newtp, err := topic.New(t)
	if err != nil {
		return err
	}
	var inStart int
	if newtp.IsReg {
		if inCount < 1 {
			return errors.New("regist reg Func, first param must exist")
		}
		if rt.In(0) != sliceType {
			return errors.New("regist reg Func, first param must []string type")
		}
		inStart = 1
		inCount = inCount - 1
	}

	argsType := make([]*argType, 0, inCount)
	for i := inStart; i < length; i++ {
		at := rt.In(i)
		argsType = append(argsType, &argType{at: at})
	}
	mType := &method{
		function:  rv,
		argsType:  argsType,
		argsCount: inCount,
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
		// 同步注册。用 go 的话，On 之后紧跟的 Emit 可能先到达服务端——
		// 那时 topic 还没注册，事件直接变成"无人监听"被丢掉，而且不报错。
		// 服务端现在也是同步处理单条连接的消息（service.HandleMsg），
		// 两端一起才真正保证顺序。
		// 连接还没起来时 regist_topic 会隔 2s 重试直到成功，所以这里会阻塞
		// 到连上为止，保证 On 返回时 topic 已在服务端就位。
		this.regist_topic()
	}
	return nil
}
