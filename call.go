package event

import "github.com/ndsky1003/event/v2/msg"

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
