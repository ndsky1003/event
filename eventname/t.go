package eventname

import "strings"

type T string

func (this T) IsReg() bool {
	s := string(this)
	return strings.HasPrefix(s, "/") && strings.HasSuffix(s, "/")
}
