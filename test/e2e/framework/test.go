/*
Copyright 2021 The KCP Authors.

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
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// we want to ensure that everything runs in parallel and that users of this framework
// do not need to explicitly remember to call the top-level testing.T method, so we do
// it for them and keep track of which we've already done so we do not cause a double-
// call panic
var seen = sync.Map{}

type RunningServer interface {
	Name() string
	KubeconfigPath() string
	RawConfig() (clientcmdapi.Config, error)
	Config() (*rest.Config, error)
	Artifact(t TestingTInterface, producer func() (runtime.Object, error))
}

type TestFunc func(t TestingTInterface, servers map[string]RunningServer, artifactDir, dataDir string)

// KcpConfig qualify a kcp server to start
type KcpConfig struct {
	Name string
	Args []string
}

// RunParallel mimics the testing.T.Run function while providing a nice set of concurrency
// guarantees for the processes that we create and manage for test cases. We ensure:
// - any kcp processes that are started will only be exposed to the test
//   code once they have signalled that they are healthy, ready, and live
// - any kcp processes will have their lifetime bound to the lifetime of the
//   individual test case - they will be cancelled when the test finishes
// - any errors in running an accessory process (other than it being cancelled by the
//   above mechanism) will be fatal to the test execution and will preempt the other
//   test routines, including failures to continue being healthy, ready and live
// This is a generally non-standard use of the testing.T construct; the two largest
// reasons we built this abstraction are that we need to manage a fairly complex and
// fragile set of concurrency and lifetime guarantees for the processes that are
// executed for each test case. We must control the scope in which each test runs, to
// be able to defer() actions that cancel background processes when a test finishes
// execution, as child goroutines are not by default killed when a test finishes and
// interaction with the parent testing.T after the test is finished causes a panic.
// Furthermore, we could not simply use the central testing.T for managing control
// flow as it is not allowed to call testing.T.FailNow (via Fatal, etc) in anything
// other than the main testing goroutine. Therefore, when more than one routine needs
// to be able to influence the execution flow (e.g. preempt other routines) we must
// have the central routine watch for incoming errors from delegate routines.
func RunParallel(top *testing.T, name string, f TestFunc, cfgs ...KcpConfig) {
	if _, previouslyCalled := seen.LoadOrStore(fmt.Sprintf("%p", top), nil); !previouslyCalled {
		top.Parallel()
	}
	top.Run(name, func(mid *testing.T) {
		// don't run in parallel if we are in INPROCESS debugging mode
		if !runKcpInProcess() {
			mid.Parallel()
		}

		ctx, cancel := context.WithCancel(context.Background())

		// Ensure servers are torn down after artifact collection by
		// registering the context cancellation (that controls server
		// exit) before the test has a chance to register artifact
		// collection.
		mid.Cleanup(cancel)

		bottom := NewT(ctx, mid)
		artifactDir, dataDir, err := ScratchDirs(bottom)
		if err != nil {
			bottom.Errorf("failed to create scratch dirs: %v", err)
			cancel()
			return
		}
		var servers []*kcpServer
		runningServers := map[string]RunningServer{}
		for _, cfg := range cfgs {
			server, err := newKcpServer(bottom, cfg, artifactDir, dataDir)
			if err != nil {
				mid.Fatal(err)
			}
			servers = append(servers, server)
			runningServers[server.name] = server
		}

		// Use a context to communicate when the test-executing
		// goroutine has finished.
		testCompleteCtx, testCompleteCancel := context.WithCancel(context.Background())

		// start all test routines separately, so the main routine can begin
		// consuming multi-threaded *testing.T calls
		go func(t TestingTInterface, cancel func()) {
			defer cancel() // stop waiting for errors

			start := time.Now()
			t.Log("Starting kcp servers...")
			// launch kcp servers and ensure they are ready before starting the test
			wg := sync.WaitGroup{}
			wg.Add(len(servers))
			for _, srv := range servers {
				// binding the server to ctx ensures its lifetime is only
				// as long as the test we are running in this specific case
				if err := srv.Run(ctx); err != nil {
					t.Error(err)
					wg.Done()
				} else {
					go func(s *kcpServer) {
						defer wg.Done()
						if err := s.Ready(); err != nil {
							t.Errorf("kcp server %s never became ready: %v", s.name, err)
						}
					}(srv)
				}
			}
			wg.Wait()

			// if we've failed during startup, don't bother running the test
			if t.Failed() {
				return
			}
			t.Logf("Started kcp servers after %s", time.Since(start))

			// run the test
			f(t, runningServers, artifactDir, dataDir)
		}(bottom, testCompleteCancel)

		<-testCompleteCtx.Done()
	})
}
