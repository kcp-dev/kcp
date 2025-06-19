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
	KnownGoroutineLeaks = []goleak.Option{
		// context
		// created by: context.(*cancelCtx).propagateCancel
		// came up ~12 times, likely a result of some of the following
		// leaks
		goleak.IgnoreTopFunction("context.(*cancelCtx).propagateCancel.func2"),

		// grpc
		// created by: google.golang.org/grpc.(*acBalancerWrapper).Connect
		goleak.IgnoreTopFunction("google.golang.org/grpc.(*addrConn).resetTransportAndUnlock"),
		// created by: go.etcd.io/etcd/client/v3.(*watcher).newWatcherGrpcStream
		goleak.IgnoreTopFunction("google.golang.org/grpc.(*pickerWrapper).pick"),
		// created by: google.golang.org/grpc/internal/grpcsync.NewCallbackSerializer
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/grpcsync.(*CallbackSerializer).run"),
		// created by: google.golang.org/grpc.newClientStreamWithParams
		goleak.IgnoreTopFunction("google.golang.org/grpc.newClientStreamWithParams.func4"),
		// created by: google.golang.org/grpc/internal/transport.(*serverHandlerTransport).HandleStreams
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/transport.(*serverHandlerTransport).HandleStreams.func1"),
		// created by: google.golang.org/grpc.newClientStreamWithParams
		goleak.IgnoreTopFunction("google.golang.org/grpc.newClientStreamWithParams.func4"),
		// created by: google.golang.org/grpc/internal/transport.NewHTTP2Client
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/transport.(*controlBuffer).get"),
		// created by: google.golang.org/grpc/internal/transport.NewHTTP2Client
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/transport.(*http2Client).keepalive"),
		// created by: go.etcd.io/etcd/server/v3/etcdserver/api/v3rpc.(*watchServer).Watch
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/transport.(*recvBufferReader).readMessageHeader"),
		// created by: go.etcd.io/etcd/client/v3.(*watchGrpcStream).newWatchClient
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/transport.(*recvBufferReader).readMessageHeaderClient"),
		// created by: google.golang.org/grpc/internal/transport.(*serverHandlerTransport).HandleStreams
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/transport.(*serverHandlerTransport).HandleStreams.func1"),
		// created by: golang.org/x/net/http2.(*serverConn).scheduleHandler
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/transport.(*serverHandlerTransport).runStream"),
		// created by: google.golang.org/grpc/internal/transport.NewHTTP2Client
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
		// created by: google.golang.org/grpc/internal/transport.(*serverHandlerTransport).HandleStreams
		goleak.IgnoreTopFunction("sync.runtime_notifyListWait"),

		// etcd
		// created by: go.etcd.io/etcd/client/v3.(*watchGrpcStream).waitCancelSubstreams.func1
		goleak.IgnoreTopFunction("go.etcd.io/etcd/client/v3.(*watchGrpcStream).waitCancelSubstreams.func1.1"),
		// created by: go.etcd.io/etcd/client/v3.(*watchGrpcStream).newWatchClient
		goleak.IgnoreTopFunction("go.etcd.io/etcd/client/v3.(*watchGrpcStream).serveSubstream.func1"),

		// kcp / kube
		// created by k8s.io/apiserver/pkg/registry/generic/registry.(*Store).startObservingCount
		goleak.IgnoreTopFunction("k8s.io/apimachinery/pkg/util/wait.BackoffUntil"),
		// created by: github.com/kcp-dev/kcp/pkg/informer.NewGenericDiscoveringDynamicSharedInformerFactory[...]
		goleak.IgnoreTopFunction("github.com/kcp-dev/kcp/pkg/informer.NewGenericDiscoveringDynamicSharedInformerFactory[...].func3"),
		// created by: k8s.io/apiserver/pkg/storage/storagebackend/factory.newETCD3Check
		goleak.IgnoreTopFunction("k8s.io/apiserver/pkg/storage/storagebackend/factory.newETCD3Check.func2"),

		// Known from kcp-dev/kcp#3350
		// created by: k8s.io/client-go/util/workqueue.newDelayingQueue[...]
		goleak.IgnoreTopFunction("k8s.io/client-go/util/workqueue.(*delayingType[...]).waitingLoop"),
		// created by: k8s.io/client-go/util/workqueue.newQueue[...]
		goleak.IgnoreTopFunction("k8s.io/client-go/util/workqueue.(*Typed[...]).updateUnfinishedWorkLoop"),

		// unknown
		// created by: net/http.(*Transport).dialConn
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
		// created by: net/http.(*Server).Serve
		goleak.IgnoreTopFunction("golang.org/x/net/http2.(*serverConn).serve"),
		// created by: gopkg.in/natefinch/lumberjack%2ev2.(*Logger).mill.func1
		goleak.IgnoreTopFunction("gopkg.in/natefinch/lumberjack%2ev2.(*Logger).millRun"),
		// created by: golang.org/x/net/http2.(*serverConn).serve
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
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
