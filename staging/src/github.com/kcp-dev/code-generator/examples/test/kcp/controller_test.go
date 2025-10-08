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

package kcp

import (
	"context"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"

	clienttesting "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	examplev1 "acme.corp/pkg/apis/example/v1"
	"acme.corp/pkg/kcp/clients/clientset/versioned/fake"
	informers "acme.corp/pkg/kcp/clients/informers/externalversions"
)

// TestFakeClient demonstrates how to use a fake client with SharedInformerFactory in tests.
func TestFakeClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcherStarted := make(chan struct{})
	// Create the fake client.
	client := fake.NewSimpleClientset()
	// A catch-all watch reactor that allows us to inject the watcherStarted channel.
	client.PrependWatchReactor("*", func(action clienttesting.Action) (handled bool, ret watch.Interface, err error) {
		gvr := action.GetResource()
		ns := action.GetNamespace()
		watch, err := client.Tracker().Watch(gvr, ns)
		if err != nil {
			return false, nil, err
		}
		close(watcherStarted)
		return true, watch, nil
	})

	// We will create an informer that writes added testTypes to a channel.
	testTypes := make(chan *examplev1.TestType, 1)
	informerFactory := informers.NewSharedInformerFactory(client, 0)
	testTypeInformer := informerFactory.Example().V1().TestTypes().Informer()
	if _, err := testTypeInformer.AddEventHandler(&cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			testType := obj.(*examplev1.TestType)
			t.Logf("testType added: %s/%s", testType.Namespace, testType.Name)
			testTypes <- testType
		},
	}); err != nil {
		t.Fatalf("Failed to add event handler: %v", err)
	}

	// Make sure informers are running.
	informerFactory.Cluster(logicalcluster.Name("root")).Start(ctx.Done())

	// This is not required in tests, but it serves as a proof-of-concept by
	// ensuring that the informer goroutine have warmed up and called List before
	// we send any events to it.
	cache.WaitForCacheSync(ctx.Done(), testTypeInformer.HasSynced)

	// The fake client doesn't support resource version. Any writes to the client
	// after the informer's initial LIST and before the informer establishing the
	// watcher will be missed by the informer. Therefore we wait until the watcher
	// starts.
	// Note that the fake client isn't designed to work with informer. It
	// doesn't support resource version. It's encouraged to use a real client
	// in an integration/E2E test if you need to test complex behavior with
	// informer/controllers.
	<-watcherStarted
	// Inject an event into the fake client.
	p := &examplev1.TestType{ObjectMeta: metav1.ObjectMeta{Name: "my-testobj"}}
	_, err := client.ExampleV1().TestTypes().Cluster(logicalcluster.NewPath("root")).Namespace("test-ns").Create(ctx, p, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("error injecting testType add: %v", err)
	}

	select {
	case testType := <-testTypes:
		t.Logf("Got testType from channel: %s/%s", testType.Namespace, testType.Name)
	case <-time.After(wait.ForeverTestTimeout):
		t.Error("Informer did not get the added testType")
	}
}
