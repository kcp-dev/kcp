package utils

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/cache"
)

func mustPanic(t *testing.T, f func()) (recovered any) {
	t.Helper()
	defer func() {
		recovered = recover()
		if recovered == nil {
			t.Fatalf("expected a panic, but none occurred")
		}
	}()
	f()
	return
}

func TestObjOrTombstone_ReturnsObjectDirectly(t *testing.T) {
	pod := &corev1.Pod{}
	got := ObjOrTombstone[*corev1.Pod](pod)
	if got != pod {
		t.Fatalf("expected same pointer back, got %#v", got)
	}
}

func TestObjOrTombstone_UnwrapsTombstone(t *testing.T) {
	pod := &corev1.Pod{}
	tomb := cache.DeletedFinalStateUnknown{
		Key: "default/my-pod",
		Obj: pod,
	}
	got := ObjOrTombstone[*corev1.Pod](tomb)
	if got != pod {
		t.Fatalf("expected unwrapped pod pointer, got %#v", got)
	}
}

func TestObjOrTombstone_PanicsOnTombstoneWrongType(t *testing.T) {
	tomb := cache.DeletedFinalStateUnknown{
		Key: "default/not-a-pod",
		Obj: &corev1.Service{},
	}
	rec := mustPanic(t, func() {
		_ = ObjOrTombstone[*corev1.Pod](tomb)
	})
	if _, ok := rec.(error); !ok {
		t.Fatalf("expected panic with error, got: %#v", rec)
	}
}

func TestObjOrTombstone_PanicsOnCompletelyWrongType(t *testing.T) {
	rec := mustPanic(t, func() {
		_ = ObjOrTombstone[*corev1.Pod](fmt.Stringer(nil))
	})
	if _, ok := rec.(error); !ok {
		t.Fatalf("expected panic with error, got: %#v", rec)
	}
}
