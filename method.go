package event

import (
	"reflect"

	"github.com/ndsky1003/event/v3/topic"
)

type method struct {
	function  reflect.Value
	argsType  []*argType
	argsCount int
}

// topicEntry 一个监听主题及其回调列表。
// Client.topics 用 slice 而非 map：topics 的所有操作都是全量遍历（On 找 Equal、
// func_call 找 Match、regist_topic 找未注册、stopLocked 重置），从没有按 key 的 O(1)
// 查表，map 的哈希能力白付；而一个客户端注册的主题通常只有个位数到几十个，线性扫足够快。
type topicEntry struct {
	tp      *topic.Topic
	methods []*method
}

type argType struct {
	at reflect.Type
}
