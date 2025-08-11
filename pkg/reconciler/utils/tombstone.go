package utils

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"
)

func ObjOrTombstone[T runtime.Object](obj any) T {
	if t, ok := obj.(T); ok {
		return t
	}
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		if t, ok := tombstone.Obj.(T); ok {
			return t
		}

		panic(fmt.Errorf("tombstone %T is not a %T", tombstone, new(T)))
	}

	panic(fmt.Errorf("%T is not a %T", obj, new(T)))
}
