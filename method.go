package event

import (
	"reflect"
)

type method struct {
	function reflect.Value
	argsType []*argType
	argCount int
}

type argType struct {
	isPointer bool
	at        reflect.Type
}
