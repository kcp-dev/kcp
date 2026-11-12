/*
Copyright 2026 The kcp Authors.

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

package replication

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	fcache "k8s.io/client-go/tools/cache/testing"
)

type testInformer struct {
	cache.SharedIndexInformer
	handlers atomic.Int32
	stop     chan struct{}
}

func (i *testInformer) AddEventHandler(h cache.ResourceEventHandler) (cache.ResourceEventHandlerRegistration, error) {
	reg, err := i.SharedIndexInformer.AddEventHandler(h)
	if err == nil {
		i.handlers.Add(1)
	}
	return reg, err
}

func (i *testInformer) RemoveEventHandler(reg cache.ResourceEventHandlerRegistration) error {
	i.handlers.Add(-1)
	return i.SharedIndexInformer.RemoveEventHandler(reg)
}

func newTestInformer(t *testing.T) *testInformer {
	t.Helper()
	i := &testInformer{
		SharedIndexInformer: cache.NewSharedIndexInformer(fcache.NewFakeControllerSource(), &unstructured.Unstructured{}, 0, cache.Indexers{}),
		stop:                make(chan struct{}),
	}
	go i.Run(i.stop)
	t.Cleanup(func() {
		if !i.IsStopped() {
			close(i.stop)
		}
	})
	return i
}

func startController(local, global *testInformer) (*Controller, chan struct{}) {
	c := NewController("root", nil, nil, nil, nil, "root", schema.GroupVersionResource{Resource: "widgets"},
		&ReplicatedGVR{Local: local, Global: global}, func() {}, Selection{})
	done := make(chan struct{})
	go func() {
		c.Start(context.Background(), 1)
		close(done)
	}()
	return c, done
}

func TestStartWithStoppedInformer(t *testing.T) {
	t.Parallel()

	local, global := newTestInformer(t), newTestInformer(t)
	close(global.stop)
	require.Eventually(t, global.IsStopped, wait.ForeverTestTimeout, tick)

	_, done := startController(local, global)
	require.Eventually(t, func() bool { return isClosed(done) }, wait.ForeverTestTimeout, tick)
	require.Zero(t, local.handlers.Load())
	require.Zero(t, global.handlers.Load())
}

func TestStartStopsWhenInformerStops(t *testing.T) {
	t.Parallel()

	local, global := newTestInformer(t), newTestInformer(t)
	c, done := startController(local, global)
	require.Eventually(t, c.Started, wait.ForeverTestTimeout, tick)

	close(local.stop)
	require.Eventually(t, func() bool { return isClosed(done) }, wait.ForeverTestTimeout, tick)
	require.False(t, c.Started())
	require.Zero(t, local.handlers.Load())
	require.Zero(t, global.handlers.Load())
}

const tick = 100 * time.Millisecond

func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
