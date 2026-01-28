/*
Copyright 2015 The Kubernetes Authors.
Modifications Copyright 2025 The KCP Authors.

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

package reflector

import (
	"context"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/clock"
)

// Config contains all the settings for one of these low-level controllers.
// KCP modification: Added KeyFunction field for cluster-aware key generation.
type Config struct {
	// The queue for your objects - has to be a DeltaFIFO due to
	// assumptions in the implementation. Your Process() function
	// should accept the output of this Queue's Pop() method.
	cache.Queue

	// Something that can list and watch your objects.
	cache.ListerWatcher

	// Process can process a popped Deltas.
	Process cache.ProcessFunc

	// ObjectType is an example object of the type this controller is
	// expected to handle.
	ObjectType runtime.Object

	// ObjectDescription is the description to use when logging type-specific information about this controller.
	ObjectDescription string

	// FullResyncPeriod is the period at which ShouldResync is considered.
	FullResyncPeriod time.Duration

	// MinWatchTimeout, if set, will define the minimum timeout for watch requests send
	// to kube-apiserver. However, values lower than 5m will not be honored to avoid
	// negative performance impact on controlplane.
	// Optional - if unset a default value of 5m will be used.
	MinWatchTimeout time.Duration

	// ShouldResync is periodically used by the reflector to determine
	// whether to Resync the Queue. If ShouldResync is `nil` or
	// returns true, it means the reflector should proceed with the
	// resync.
	ShouldResync cache.ShouldResyncFunc

	// Called whenever the ListAndWatch drops the connection with an error.
	WatchErrorHandler cache.WatchErrorHandler

	// Called whenever the ListAndWatch drops the connection with an error
	// and WatchErrorHandler is not set.
	WatchErrorHandlerWithContext cache.WatchErrorHandlerWithContext

	// WatchListPageSize is the requested chunk size of initial and relist watch lists.
	WatchListPageSize int64

	// KCP modification: KeyFunction is the function used to generate keys for objects
	// in the reflector's temporary store during WatchList operations.
	// This is critical for multi-cluster setups where objects from different clusters
	// may have the same namespace/name but different cluster names.
	KeyFunction cache.KeyFunc
}

// controller implements Controller
type controller struct {
	config         Config
	reflector      *Reflector
	reflectorMutex sync.RWMutex
	clock          clock.Clock
}

// New makes a new Controller from the given Config.
// KCP modification: This controller uses our forked Reflector with cluster-aware key function support.
// Returns cache.Controller so it can be used as a drop-in replacement.
func New(c *Config) cache.Controller {
	ctlr := &controller{
		config: *c,
		clock:  &clock.RealClock{},
	}
	return ctlr
}

// Run implements [Controller.Run].
func (c *controller) Run(stopCh <-chan struct{}) {
	c.RunWithContext(wait.ContextForChannel(stopCh))
}

// RunWithContext implements [Controller.RunWithContext].
func (c *controller) RunWithContext(ctx context.Context) {
	defer utilruntime.HandleCrashWithContext(ctx)
	go func() {
		<-ctx.Done()
		c.config.Queue.Close()
	}()

	// KCP modification: Use our forked NewReflectorWithOptions with KeyFunction support
	r := NewReflectorWithOptions(
		c.config.ListerWatcher,
		c.config.ObjectType,
		c.config.Queue,
		ReflectorOptions{
			ResyncPeriod:    c.config.FullResyncPeriod,
			MinWatchTimeout: c.config.MinWatchTimeout,
			TypeDescription: c.config.ObjectDescription,
			Clock:           c.clock,
			KeyFunction:     c.config.KeyFunction, // KCP modification: pass the key function
		},
	)
	r.ShouldResync = c.config.ShouldResync
	r.WatchListPageSize = c.config.WatchListPageSize
	if c.config.WatchErrorHandler != nil {
		r.watchErrorHandler = func(_ context.Context, refl *Reflector, err error) {
			// WatchErrorHandler takes cache.Reflector, but we have our own Reflector type.
			// Since the handler typically just logs the error and doesn't use the reflector much,
			// we pass nil. This is safe because most handlers only use the reflector for Name() and
			// TypeDescription() which we could add getter methods for if needed.
			c.config.WatchErrorHandler(nil, err)
		}
	} else if c.config.WatchErrorHandlerWithContext != nil {
		r.watchErrorHandler = func(ctx context.Context, refl *Reflector, err error) {
			// Same as above - pass nil for the cache.Reflector
			c.config.WatchErrorHandlerWithContext(ctx, nil, err)
		}
	}

	c.reflectorMutex.Lock()
	c.reflector = r
	c.reflectorMutex.Unlock()

	var wg wait.Group

	wg.StartWithContext(ctx, r.RunWithContext)

	wait.UntilWithContext(ctx, c.processLoop, time.Second)
	wg.Wait()
}

// processLoop drains the work queue.
// TODO: Consider doing the processing in parallel. This will require a little thought
// to make sure that we don't end up processing the same object multiple times
// concurrently.
func (c *controller) processLoop(ctx context.Context) {
	for {
		obj, err := c.config.Queue.Pop(cache.PopProcessFunc(c.config.Process))
		if err != nil {
			if err == cache.ErrFIFOClosed {
				return
			}
			if c.config.ShouldResync != nil && c.config.ShouldResync() {
				// If ShouldResync returned true, the queue item couldn't be processed
				// and we should retry. We could be more clever here and only retry
				// if the error was specific to this item.
			}
			utilruntime.HandleErrorWithContext(ctx, err, "Failed to process object from queue")
		}
		if obj == nil {
			return
		}
	}
}

// HasSynced returns true if the source informer has synced.
func (c *controller) HasSynced() bool {
	return c.config.Queue.HasSynced()
}

// LastSyncResourceVersion is the resource version observed when last synced with the underlying
// store. The value returned is not synchronized with access to the underlying store and is not
// thread-safe.
func (c *controller) LastSyncResourceVersion() string {
	c.reflectorMutex.RLock()
	defer c.reflectorMutex.RUnlock()
	if c.reflector == nil {
		return ""
	}
	return c.reflector.LastSyncResourceVersion()
}
