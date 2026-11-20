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
		// 连接还没起来时 regist_topic 会立刻返回，connect 成功后再补注册，
		// 所以这里不会阻塞在"没连接"上。
		this.regist_topic()
	}
	return nil
}
