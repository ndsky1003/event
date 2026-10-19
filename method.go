package event

import (
	"reflect"
)

type method struct {
	function  reflect.Value
	argsType  []*argType
	argsCount int
}

type argType struct {
	at reflect.Type
}
