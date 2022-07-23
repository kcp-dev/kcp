/*
Copyright 2022 The KCP Authors.

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

package authorization

import (
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/storage"
	"k8s.io/kubernetes/pkg/controller"

	workspaceapi "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	workspaceapiv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	tenancyfake "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/fake"
	tenancyInformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	workspacecache "github.com/kcp-dev/kcp/pkg/virtual/workspaces/cache"
	workspaceutil "github.com/kcp-dev/kcp/pkg/virtual/workspaces/util"
)

type mockClusterClient struct {
	mockClient kcpclient.Interface
}

func (m *mockClusterClient) Cluster(name logicalcluster.Name) kcpclient.Interface {
	return m.mockClient
}

var _ kcpclient.ClusterInterface = (*mockClusterClient)(nil)

func newTestWatcher(username string, groups []string, predicate storage.SelectionPredicate, workspaces ...*workspaceapi.ClusterWorkspace) (*userWorkspaceWatcher, *fakeAuthCache) {
	objects := []runtime.Object{}
	for i := range workspaces {
		objects = append(objects, workspaces[i])
	}
	mockClient := tenancyfake.NewSimpleClientset(objects...)

	informers := tenancyInformers.NewSharedInformerFactory(mockClient, controller.NoResyncPeriodFunc())
	workspaceCache := workspacecache.NewClusterWorkspaceCache(
		informers.Tenancy().V1alpha1().ClusterWorkspaces().Informer(),
		&mockClusterClient{mockClient: mockClient},
	)
	fakeAuthCache := &fakeAuthCache{}

	return NewUserWorkspaceWatcher(&user.DefaultInfo{Name: username, Groups: groups}, logicalcluster.New("lclusterName"), workspaceCache, fakeAuthCache, false, predicate), fakeAuthCache
}

type fakeAuthCache struct {
	clusterWorkspaces []*workspaceapi.ClusterWorkspace

	removed []CacheWatcher
}

func (w *fakeAuthCache) RemoveWatcher(watcher CacheWatcher) {
	w.removed = append(w.removed, watcher)
}

func (w *fakeAuthCache) List(userInfo user.Info, labelSelector labels.Selector, fieldSelector fields.Selector) (*workspaceapi.ClusterWorkspaceList, error) {
	ret := &workspaceapi.ClusterWorkspaceList{}
	if w.clusterWorkspaces != nil {
		for i := range w.clusterWorkspaces {
			ret.Items = append(ret.Items, *w.clusterWorkspaces[i])
		}
	}

	return ret, nil
}

func TestFullIncoming(t *testing.T) {
	watcher, fakeAuthCache := newTestWatcher("bob", nil, matchAllPredicate(), newClusterWorkspaces("ns-01")...)
	watcher.cacheIncoming = make(chan watch.Event)

	go watcher.Watch()
	watcher.cacheIncoming <- watch.Event{Type: watch.Added}

	// this call should not block and we should see a failure
	watcher.GroupMembershipChanged("ns-01", sets.NewString("bob"), sets.String{})
	if len(fakeAuthCache.removed) != 1 {
		t.Errorf("should have removed self")
	}

	err := wait.PollImmediate(10*time.Millisecond, 5*time.Second, func() (done bool, err error) {
		if len(watcher.cacheError) > 0 {
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	for {
		repeat := false
		select {
		case event, ok := <-watcher.ResultChan():
			if !ok {
				t.Fatalf("channel closed")
			}
			// this happens when the cacheIncoming block wins the select race
			if event.Type == watch.Added {
				repeat = true
				break
			}
			// this should be an error
			if event.Type != watch.Error {
				t.Errorf("expected error, got %v", event)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout")
		}
		if !repeat {
			break
		}
	}
}

func TestAddModifyDeleteEventsByUser(t *testing.T) {
	watcher, _ := newTestWatcher("bob", nil, matchAllPredicate(), newClusterWorkspaces("ns-01")...)
	go watcher.Watch()

	watcher.GroupMembershipChanged("ns-01", sets.NewString("bob"), sets.String{})
	select {
	case event := <-watcher.ResultChan():
		if event.Type != watch.Added {
			t.Errorf("expected added, got %v", event)
		}
		if event.Object.(*workspaceapiv1beta1.Workspace).Name != "ns-01" {
			t.Errorf("expected %v, got %#v", "ns-01", event.Object)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout")
	}

	// the object didn't change, we shouldn't observe it
	watcher.GroupMembershipChanged("ns-01", sets.NewString("bob"), sets.String{})
	select {
	case event := <-watcher.ResultChan():
		t.Fatalf("unexpected event %v", event)
	case <-time.After(3 * time.Second):
	}

	watcher.GroupMembershipChanged("ns-01", sets.NewString("alice"), sets.String{})
	select {
	case event := <-watcher.ResultChan():
		if event.Type != watch.Deleted {
			t.Errorf("expected Deleted, got %v", event)
		}
		if event.Object.(*workspaceapiv1beta1.Workspace).Name != "ns-01" {
			t.Errorf("expected %v, got %#v", "ns-01", event.Object)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout")
	}
}

func TestWorkspaceSelectionPredicate(t *testing.T) {
	field := fields.ParseSelectorOrDie("metadata.name=ns-03")
	m := workspaceutil.MatchWorkspace(labels.Everything(), field)

	watcher, _ := newTestWatcher("bob", nil, m, newClusterWorkspaces("ns-01", "ns-02", "ns-03")...)

	if watcher.emit == nil {
		t.Fatalf("unset emit function")
	}

	go watcher.Watch()

	// a workspace we did not select changed, we shouldn't observe it
	watcher.GroupMembershipChanged("ns-01", sets.NewString("bob"), sets.String{})
	select {
	case event := <-watcher.ResultChan():
		t.Fatalf("unexpected event %v", event)
	case <-time.After(3 * time.Second):
	}

	watcher.GroupMembershipChanged("ns-03", sets.NewString("bob"), sets.String{})
	select {
	case event := <-watcher.ResultChan():
		if event.Type != watch.Added {
			t.Errorf("expected added, got %v", event)
		}
		if event.Object.(*workspaceapiv1beta1.Workspace).Name != "ns-03" {
			t.Errorf("expected %v, got %#v", "ns-03", event.Object)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout")
	}

	// the object didn't change, we shouldn't observe it
	watcher.GroupMembershipChanged("ns-03", sets.NewString("bob"), sets.String{})
	select {
	case event := <-watcher.ResultChan():
		t.Fatalf("unexpected event %v", event)
	case <-time.After(3 * time.Second):
	}

	// deletion occurred in a separate workspace, we should not observe it
	watcher.GroupMembershipChanged("ns-01", sets.NewString("alice"), sets.String{})
	select {
	case event := <-watcher.ResultChan():
		t.Fatalf("unexpected event %v", event)
	case <-time.After(3 * time.Second):
	}

	// deletion occurred in selected workspace, we should observe it
	watcher.GroupMembershipChanged("ns-03", sets.NewString("alice"), sets.String{})
	select {
	case event := <-watcher.ResultChan():
		if event.Type != watch.Deleted {
			t.Errorf("expected Deleted, got %v", event)
		}
		if event.Object.(*workspaceapiv1beta1.Workspace).Name != "ns-03" {
			t.Errorf("expected %v, got %#v", "ns-03", event.Object)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout")
	}
}

func TestAddModifyDeleteEventsByGroup(t *testing.T) {
	watcher, _ := newTestWatcher("bob", []string{"group-one"}, matchAllPredicate(), newClusterWorkspaces("ns-01")...)
	go watcher.Watch()

	watcher.GroupMembershipChanged("ns-01", sets.String{}, sets.NewString("group-one"))
	select {
	case event := <-watcher.ResultChan():
		if event.Type != watch.Added {
			t.Errorf("expected added, got %v", event)
		}
		if event.Object.(*workspaceapiv1beta1.Workspace).Name != "ns-01" {
			t.Errorf("expected %v, got %#v", "ns-01", event.Object)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout")
	}

	// the object didn't change, we shouldn't observe it
	watcher.GroupMembershipChanged("ns-01", sets.String{}, sets.NewString("group-one"))
	select {
	case event := <-watcher.ResultChan():
		t.Fatalf("unexpected event %v", event)
	case <-time.After(3 * time.Second):
	}

	watcher.GroupMembershipChanged("ns-01", sets.String{}, sets.NewString("group-two"))
	select {
	case event := <-watcher.ResultChan():
		if event.Type != watch.Deleted {
			t.Errorf("expected Deleted, got %v", event)
		}
		if event.Object.(*workspaceapiv1beta1.Workspace).Name != "ns-01" {
			t.Errorf("expected %v, got %#v", "ns-01", event.Object)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout")
	}
}

func newClusterWorkspaces(names ...string) []*workspaceapi.ClusterWorkspace {
	ret := []*workspaceapi.ClusterWorkspace{}
	for _, name := range names {
		ret = append(ret, &workspaceapi.ClusterWorkspace{ObjectMeta: metav1.ObjectMeta{Name: name}})
	}

	return ret
}

func matchAllPredicate() storage.SelectionPredicate {
	return workspaceutil.MatchWorkspace(labels.Everything(), fields.Everything())
}
