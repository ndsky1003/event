package msg

import (
	"regexp"
	"strings"
)

// HasPrefix("//") 是一个正在表达式
type EventType string

func (this EventType) isRegexp() bool {
	s := string(this)
	return strings.HasPrefix(s, "/") && strings.HasSuffix(s, "/")
}

// 引入这个概念是为了推广正则,有了正则就不用拓展组
type EventTopic struct {
	et       EventType
	reg      *regexp.Regexp
	isRegexp bool
}

func NewEventTopic(et EventType) *EventTopic {
	s := &EventTopic{
		et: et,
	}
	str := string(et)
	length := len(str)
	if et.isRegexp() {
		s.reg = regexp.MustCompile(str[1 : length-1])
		s.isRegexp = true
	}
	return s
}

func (this *EventTopic) Match(et EventType) bool {
	if this.isRegexp {
		return this.reg.MatchString(string(et))
	} else {
		return this.et == et
	}
}

func (this *EventTopic) Equal(et EventType) bool {
	return this.et == et
}

func (this *EventTopic) GetEventType() EventType {
	return this.et
}

func (this *EventTopic) FindStringSubmatch(et EventType) []string {

	if this.isRegexp {
		return this.reg.FindStringSubmatch(string(et))
	} else {
		return []string{string(et)}
	}

}
