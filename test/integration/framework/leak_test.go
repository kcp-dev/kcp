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
	// knownGoroutineLeaks are leaks from just running and stopping kcp
	// collected and run through:
	//	grep 'on top of the stack' output.log | cut -d, -f2- | cut -d' ' -f3 | sort | uniq
	knownGoroutineLeaks = []goleak.Option{
		goleak.IgnoreTopFunction("github.com/kcp-dev/kcp/pkg/informer.NewGenericDiscoveringDynamicSharedInformerFactory[...].func3"),
		goleak.IgnoreTopFunction("google.golang.org/grpc.(*addrConn).resetTransportAndUnlock"),
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/grpcsync.(*CallbackSerializer).run"),
		goleak.IgnoreTopFunction("k8s.io/apimachinery/pkg/util/wait.BackoffUntil"),
		goleak.IgnoreTopFunction("k8s.io/apiserver/pkg/storage/storagebackend/factory.newETCD3Check.func2"),
		goleak.IgnoreTopFunction("k8s.io/client-go/util/workqueue.(*delayingType[...]).waitingLoop"),
		goleak.IgnoreTopFunction("k8s.io/client-go/util/workqueue.(*Typed[...]).updateUnfinishedWorkLoop"),
		goleak.IgnoreTopFunction("k8s.io/apimachinery/pkg/util/wait.BackoffUntilWithContext"),
	}
)

func TestGoleakWithDefaults(t *testing.T) {
	curGoroutines := goleak.IgnoreCurrent()
	server, _, _ := StartTestServer(t)

	// See https://github.com/kcp-dev/kcp/issues/3488
	time.Sleep(2 * time.Second)

	server.Stop()

	// 14s as etcd sets a client request timeout of up to 7 seconds when
	// shutting down the server and then starts shutting down everything
	// else.
	// A shorter timespan would lead to false positives in the tests.
	time.Sleep(14 * time.Second)

	goleak.VerifyNone(t, append(knownGoroutineLeaks, curGoroutines)...)
}
