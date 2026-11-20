package event

import (
	"github.com/ndsky1003/event/v2/eventname"
	"github.com/ndsky1003/event/v2/msgtype"
)

type builder struct {
	client *Client
	t      msgtype.T
	en     eventname.T
	opt    SendOption
	args   []any
}

func (this *builder) BuildMsgType(t msgtype.T) *builder {
	this.t = t
	return this
}

func (this *builder) BuildEventName(en eventname.T) *builder {
	this.en = en
	return this
}

func (this *builder) BuildCoder(opts ...SendOption) *builder {
	return this
}

func (this *builder) BuildArgs(args ...any) *builder {
	this.args = args
	return this
}

func (this *builder) Send() (call *Call) {
	call = this.client.emit_async(this.t, this.en, *this.ct, this.args...)
	return
}
