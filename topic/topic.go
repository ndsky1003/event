package topic

import (
	"regexp"

	"github.com/ndsky1003/event/v2/eventname"
)

// 引入这个概念是为了推广正则,有了正则就不用拓展组
type Topic struct {
	et              eventname.T
	reg             *regexp.Regexp
	IsReg           bool
	IsRegistSuccess bool
}

func New(et eventname.T) (t *Topic, err error) {
	if et.IsReg() {
		var r *regexp.Regexp
		str := string(et)
		length := len(str)
		r, err = regexp.Compile(str[1 : length-1])
		if err != nil {
			return
		}
		t = &Topic{
			et:    et,
			reg:   r,
			IsReg: true,
		}
		return
	}
	t = &Topic{
		et: et,
	}
	return
}

func (this *Topic) Match(et eventname.T) bool {
	if this.IsReg {
		return this.reg.MatchString(string(et))
	} else {
		return this.et == et
	}
}

func (this *Topic) Equal(en eventname.T) bool {
	return this.et == en
}

func (this *Topic) GetEventName() eventname.T {
	return this.et
}

func (this *Topic) FindStringSubmatch(et eventname.T) []string {

	if this.IsReg {
		return this.reg.FindStringSubmatch(string(et))
	} else {
		return []string{string(et)}
	}

}
