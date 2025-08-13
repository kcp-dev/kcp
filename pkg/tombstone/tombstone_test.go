/*
Copyright 2025 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package tombstone

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

func TestObj_ReturnsObjectDirectly(t *testing.T) {
	pod := &corev1.Pod{}
	got := Obj[*corev1.Pod](pod)
	if got != pod {
		t.Fatalf("expected same pointer back, got %#v", got)
	}
}

func TestObj_UnwrapsTombstone(t *testing.T) {
	pod := &corev1.Pod{}
	tomb := cache.DeletedFinalStateUnknown{
		Key: "default/my-pod",
		Obj: pod,
	}
	got := Obj[*corev1.Pod](tomb)
	if got != pod {
		t.Fatalf("expected unwrapped pod pointer, got %#v", got)
	}
}

func TestObj_PanicsOnTombstoneWrongType(t *testing.T) {
	tomb := cache.DeletedFinalStateUnknown{
		Key: "default/not-a-pod",
		Obj: &corev1.Service{},
	}
	rec := mustPanic(t, func() {
		_ = Obj[*corev1.Pod](tomb)
	})
	if _, ok := rec.(error); !ok {
		t.Fatalf("expected panic with error, got: %#v", rec)
	}
}

func TestObj_PanicsOnCompletelyWrongType(t *testing.T) {
	rec := mustPanic(t, func() {
		_ = Obj[*corev1.Pod](fmt.Stringer(nil))
	})
	if _, ok := rec.(error); !ok {
		t.Fatalf("expected panic with error, got: %#v", rec)
	}
}
