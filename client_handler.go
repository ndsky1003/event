package event

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"reflect"

	"github.com/ndsky1003/event/v2/eventname"
	"github.com/vmihailenco/msgpack/v5"
	"github.com/ndsky1003/event/v2/msg"
	"github.com/ndsky1003/event/v2/msgtype"
	"github.com/ndsky1003/event/v2/topic"
)

// eventHandler 实现 conn.Handler 接口
type eventHandler struct {
	client *Client
}

// HandleMsg 处理收到的消息
func (h *eventHandler) HandleMsg(data []byte) error {
	// 解码消息
	gotMsg, err := decodeMsg(data)
	if err != nil {
		return err
	}

	switch gotMsg.T {
	case msgtype.ReqAll, msgtype.ReqOne, msgtype.ReqFirst:
		go h.funcCall(gotMsg)
	case msgtype.Res, msgtype.ResFirst, msgtype.On:
		seq := gotMsg.Seq
		h.client.l.Lock()
		call := h.client.pending[seq]
		delete(h.client.pending, seq)
		h.client.l.Unlock()
		if call != nil {
			if gotMsg.Err != "" {
				var err error
				if *h.client.opt.IsWrapError {
					err = fmt.Errorf("%w,%v", ErrServer, gotMsg.Err)
				} else {
					err = errors.New(gotMsg.Err)
				}
				call.Err = err
			}
			call.done()
		}
	}
	return nil
}

// funcCall 处理函数调用
func (h *eventHandler) funcCall(req *msg.Msg) {
	et := req.EventName
	t := msgtype.Res
	if req.T == msgtype.ReqFirst {
		t = msgtype.ResFirst
	}
	res := &msg.Msg{
		T:         t,
		Seq:       req.Seq,
		EventName: et,
	}

	h.client.rwl.RLock()
	// 预分配切片容量，减少扩容
	var matchedTopics []*topic.Topic
	var matchedMethods [][]*method
	for tp, funcs := range h.client.topics {
		if tp.Match(et) {
			matchedTopics = append(matchedTopics, tp)
			matchedMethods = append(matchedMethods, funcs)
		}
	}
	h.client.rwl.RUnlock()

	if len(matchedTopics) > 0 {
		// 预分配错误切片
		errs := make([]error, 0, len(matchedTopics))
		for i, tp := range matchedTopics {
			methods := matchedMethods[i]
			for _, method := range methods {
				var err error
				// 优化路径：Handler 接口直接调用
				if method.isHandler {
					err = h.callHandlerFast(method, req.Bytes, int(req.BodyCount), tp.IsReg, et)
				} else {
					// 兼容路径：反射调用
					err = h.callHandlerReflect(method, req.Bytes, int(req.BodyCount), tp.IsReg, et)
				}

				if err != nil {
					errs = append(errs, err)
				}
			}
		}
		if len(errs) > 0 {
			res.Err = errors.Join(errs...).Error()
		}
	}

	if err := h.client.Write(res); err != nil {
		slog.Error("write response failed", "err", err)
	}
}

// parse 解析请求参数
func (h *eventHandler) parse(reqBodyData []byte, reqArgsCount int, m *method) ([]reflect.Value, error) {
	argsValue := make([]reflect.Value, m.argsCount)
	dec := msgpack.GetDecoder()
	defer msgpack.PutDecoder(dec)

	reader := readerPool.Get().(*bytes.Reader)
	reader.Reset(reqBodyData)
	dec.Reset(reader)
	defer readerPool.Put(reader)

	for i := 0; i < m.argsCount; i++ {
		argType := m.argsType[i]
		at := argType.at
		if argType.isPointer {
			at = at.Elem()
		}
		argValue := reflect.New(at)
		if i < reqArgsCount {
			if err := dec.Decode(argValue.Interface()); err != nil {
				ft := m.function.Type()
				return nil, fmt.Errorf("%w,parse func[%v] body [%v] arg err:%w", ErrClient, ft, i, err)
			}
		}
		if !argType.isPointer {
			argValue = argValue.Elem()
		}
		argsValue[i] = argValue
	}
	return argsValue, nil
}

// callHandlerFast 快速调用路径 - Handler 接口
func (h *eventHandler) callHandlerFast(m *method, reqBodyData []byte, reqArgsCount int, isReg bool, et eventname.T) error {
	handler := m.handlerInterface.(Handler)

	// 解析参数
	var args []any
	if reqArgsCount > 0 && len(reqBodyData) > 0 {
		args = make([]any, reqArgsCount)
		dec := msgpack.GetDecoder()
		defer msgpack.PutDecoder(dec)

		reader := readerPool.Get().(*bytes.Reader)
		reader.Reset(reqBodyData)
		dec.Reset(reader)
		defer readerPool.Put(reader)

		for i := 0; i < reqArgsCount; i++ {
			if err := dec.Decode(&args[i]); err != nil {
				return err
			}
		}
	}

	// 调用 Handler
	var err error
	if isReg {
		// 正则匹配模式，需要找到匹配的 topic 来获取 submatch
		h.client.rwl.RLock()
		var tp *topic.Topic
		for existTp := range h.client.topics {
			if existTp.Match(et) {
				tp = existTp
				break
			}
		}
		h.client.rwl.RUnlock()

		var submatch []string
		if tp != nil {
			submatch = tp.FindStringSubmatch(et)
		}
		err = handler.Handle(append([]any{submatch}, args...)...)
	} else {
		err = handler.Handle(args...)
	}

	if err != nil && *h.client.opt.IsWrapError {
		return fmt.Errorf("%w: handler error: %w", ErrClient, err)
	}
	return err
}

// callHandlerReflect 反射调用路径 - 兼容模式
func (h *eventHandler) callHandlerReflect(m *method, reqBodyData []byte, reqArgsCount int, isReg bool, et eventname.T) error {
	args, err := h.parse(reqBodyData, reqArgsCount, m)
	if err != nil {
		return err
	}

	if isReg {
		// 正则匹配模式，需要找到匹配的 topic 来获取 submatch
		h.client.rwl.RLock()
		var tp *topic.Topic
		for existTp := range h.client.topics {
			if existTp.Match(et) {
				tp = existTp
				break
			}
		}
		h.client.rwl.RUnlock()

		var submatch []string
		if tp != nil {
			submatch = tp.FindStringSubmatch(et)
		}
		args = append([]reflect.Value{reflect.ValueOf(submatch)}, args...)
	}

	returnValues := m.function.Call(args)
	if len(returnValues) > 0 {
		errValue := returnValues[0]
		if errValue.IsValid() && !errValue.IsNil() {
			return errValue.Interface().(error)
		}
	}
	return nil
}
