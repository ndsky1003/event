package topic

import (
	"regexp"

	"github.com/ndsky1003/event/v3/eventname"
)

// 引入这个概念是为了推广正则,有了正则就不用拓展组
type Topic struct {
	en              eventname.T
	reg             *regexp.Regexp
	IsReg           bool
	IsRegistSuccess bool
}

func New(et eventname.T) *Topic {
	s := &Topic{
		en: et,
	}
	str := string(et)
	length := len(str)
	if et.IsReg() {
		s.reg = regexp.MustCompile(str[1 : length-1])
		s.IsReg = true
	}
	return s
}

func (this *Topic) Match(et eventname.T) bool {
	if this.IsReg {
		return this.reg.MatchString(string(et))
	} else {
		return this.en == et
	}
}

func (this *Topic) Equal(en eventname.T) bool {
	return this.en == en
}

func (this *Topic) GetEventName() eventname.T {
	return this.en
}

func (this *Topic) FindStringSubmatch(et eventname.T) []string {

	if this.IsReg {
		return this.reg.FindStringSubmatch(string(et))
	} else {
		return []string{string(et)}
	}

}
