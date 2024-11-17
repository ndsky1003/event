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
	isPointer bool
	at        reflect.Type
}
