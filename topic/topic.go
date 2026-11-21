package topic

import "github.com/ndsky1003/event/v3/eventname"

// Topic 精确匹配的订阅主题。p2p 模式不再支持正则 topic（无法用订阅表精确路由）。
type Topic struct {
	et eventname.T
}

func New(et eventname.T) (t *Topic, err error) {
	return &Topic{et: et}, nil
}

func (this *Topic) Match(et eventname.T) bool {
	return this.et == et
}

func (this *Topic) Equal(en eventname.T) bool {
	return this.et == en
}

func (this *Topic) GetEventName() eventname.T {
	return this.et
}
