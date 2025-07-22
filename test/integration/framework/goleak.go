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

package framework

import (
	"testing"
	"time"

	"go.uber.org/goleak"
)

var (
	// KnownGoroutineLeaks are leaks from just running KCP collected
	// with TestGoleakWithDefaults and run through:
	//	grep 'on top of the stack' output.log | cut -d, -f2- | cut -d' ' -f3 | sort | uniq
	KnownGoroutineLeaks = []goleak.Option{
		goleak.IgnoreTopFunction("github.com/kcp-dev/kcp/pkg/informer.NewGenericDiscoveringDynamicSharedInformerFactory[...].func3"),
		goleak.IgnoreTopFunction("google.golang.org/grpc.(*addrConn).resetTransportAndUnlock"),
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/grpcsync.(*CallbackSerializer).run"),
		goleak.IgnoreTopFunction("k8s.io/apimachinery/pkg/util/wait.BackoffUntil"),
		goleak.IgnoreTopFunction("k8s.io/apiserver/pkg/storage/storagebackend/factory.newETCD3Check.func2"),
		goleak.IgnoreTopFunction("k8s.io/client-go/util/workqueue.(*delayingType[...]).waitingLoop"),
		goleak.IgnoreTopFunction("k8s.io/client-go/util/workqueue.(*Typed[...]).updateUnfinishedWorkLoop"),
	}

	// 14s as etcd sets a client request timeout of up to 7 seconds when
	// shutting down the server and then starts shutting down everything
	// else.
	// A shorter timespan d would lead to false positives in the tests.
	WaitTime = 14 * time.Second
)

// GoleakWithDefaults verifies that there are no goroutine leaks.
// Goleak tests cannot be run in parallelized tests.
func GoleakWithDefaults(tb testing.TB, in ...goleak.Option) {
	tb.Helper()

	tb.Logf("waiting %v for goroutines to finish", WaitTime)
	time.Sleep(WaitTime)

	opts := append(KnownGoroutineLeaks, in...)
	goleak.VerifyNone(tb, opts...)
}
