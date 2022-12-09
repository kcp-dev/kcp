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
	"context"
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

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	workspaceapiv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	kcpfakeclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster/fake"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	workspacecache "github.com/kcp-dev/kcp/pkg/virtual/workspaces/cache"
	workspaceutil "github.com/kcp-dev/kcp/pkg/virtual/workspaces/util"
)

func newTestWatcher(username string, groups []string, predicate storage.SelectionPredicate, workspaces ...*tenancyv1alpha1.ClusterWorkspace) (*userWorkspaceWatcher, *fakeAuthCache) {
	objects := []runtime.Object{}
	for i := range workspaces {
		objects = append(objects, workspaces[i])
	}
	mockClient := kcpfakeclient.NewSimpleClientset(objects...)

	informers := kcpinformers.NewSharedInformerFactory(mockClient, controller.NoResyncPeriodFunc())
	workspaceCache := workspacecache.NewClusterWorkspaceCache(
		informers.Tenancy().V1alpha1().ClusterWorkspaces(),
		mockClient,
	)
	fakeAuthCache := &fakeAuthCache{}
	go informers.Start(context.Background().Done())
	informers.WaitForCacheSync(context.Background().Done())

	return NewUserWorkspaceWatcher(
			&user.DefaultInfo{Name: username, Groups: groups},
			logicalcluster.New("root"),
			workspaceCache,
			fakeAuthCache,
			false,
			predicate),
		fakeAuthCache
}

type fakeAuthCache struct {
	clusterWorkspaces []*tenancyv1alpha1.ClusterWorkspace

	removed []CacheWatcher
}

func (w *fakeAuthCache) RemoveWatcher(watcher CacheWatcher) {
	w.removed = append(w.removed, watcher)
}

func (w *fakeAuthCache) List(userInfo user.Info, labelSelector labels.Selector, fieldSelector fields.Selector) (*tenancyv1alpha1.ClusterWorkspaceList, error) {
	ret := &tenancyv1alpha1.ClusterWorkspaceList{}
	if w.clusterWorkspaces != nil {
		for i := range w.clusterWorkspaces {
			ret.Items = append(ret.Items, *w.clusterWorkspaces[i])
		}
	}

	return ret, nil
}

func TestFullIncoming(t *testing.T) {
	watcher, fakeAuthCache := newTestWatcher("bob", nil, matchAllPredicate(), newClusterWorkspaces("root:ws-01")...)
	watcher.cacheIncoming = make(chan watch.Event)

	go watcher.Watch()
	watcher.cacheIncoming <- watch.Event{Type: watch.Added}

	// this call should not block and we should see a failure
	watcher.GroupMembershipChanged("root|ws-01", sets.NewString("bob"), sets.String{})
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
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("timeout")
		}
		if !repeat {
			break
		}
	}
}

func TestAddModifyDeleteEventsByUser(t *testing.T) {
	watcher, _ := newTestWatcher("bob", nil, matchAllPredicate(), newClusterWorkspaces("root:ws-01")...)
	go watcher.Watch()

	watcher.GroupMembershipChanged("root|ws-01", sets.NewString("bob"), sets.String{})
	select {
	case event := <-watcher.ResultChan():
		if event.Type != watch.Added {
			t.Errorf("expected added, got %v", event)
		}
		if logicalcluster.From(event.Object.(*workspaceapiv1beta1.Workspace)).String() != "root" {
			t.Errorf("expected %v, got %#v", "root", event.Object)
		}
		if event.Object.(*workspaceapiv1beta1.Workspace).Name != "ws-01" {
			t.Errorf("expected %v, got %#v", "ws-01", event.Object)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("timeout")
	}

	// the object didn't change, we shouldn't observe it
	watcher.GroupMembershipChanged("root|ws-01", sets.NewString("bob"), sets.String{})
	select {
	case event := <-watcher.ResultChan():
		t.Fatalf("unexpected event %v", event)
	case <-time.After(100 * time.Millisecond):
	}

	watcher.GroupMembershipChanged("root|ws-01", sets.NewString("alice"), sets.String{})
	select {
	case event := <-watcher.ResultChan():
		if event.Type != watch.Deleted {
			t.Errorf("expected Deleted, got %v", event)
		}
		if logicalcluster.From(event.Object.(*workspaceapiv1beta1.Workspace)).String() != "root" {
			t.Errorf("expected %v, got %#v", "root", event.Object)
		}
		if event.Object.(*workspaceapiv1beta1.Workspace).Name != "ws-01" {
			t.Errorf("expected %v, got %#v", "ws-01", event.Object)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("timeout")
	}
}

func TestWorkspaceSelectionPredicate(t *testing.T) {
	field := fields.ParseSelectorOrDie("metadata.name=ws-03")
	m := workspaceutil.MatchWorkspace(labels.Everything(), field)

	watcher, _ := newTestWatcher("bob", nil, m, newClusterWorkspaces("root:ws-01", "root:ws-02", "root:ws-03")...)

	if watcher.emit == nil {
		t.Fatalf("unset emit function")
	}

	go watcher.Watch()

	// a workspace we did not select changed, we shouldn't observe it
	watcher.GroupMembershipChanged("root|ws-01", sets.NewString("bob"), sets.String{})
	select {
	case event := <-watcher.ResultChan():
		t.Fatalf("unexpected event %v", event)
	case <-time.After(100 * time.Millisecond):
	}

	watcher.GroupMembershipChanged("root|ws-03", sets.NewString("bob"), sets.String{})
	select {
	case event := <-watcher.ResultChan():
		if event.Type != watch.Added {
			t.Errorf("expected added, got %v", event)
		}
		if logicalcluster.From(event.Object.(*workspaceapiv1beta1.Workspace)).String() != "root" {
			t.Errorf("expected %v, got %#v", "root", event.Object)
		}
		if event.Object.(*workspaceapiv1beta1.Workspace).Name != "ws-03" {
			t.Errorf("expected %v, got %#v", "ws-03", event.Object)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("timeout")
	}

	// the object didn't change, we shouldn't observe it
	watcher.GroupMembershipChanged("root|ws-03", sets.NewString("bob"), sets.String{})
	select {
	case event := <-watcher.ResultChan():
		t.Fatalf("unexpected event %v", event)
	case <-time.After(100 * time.Millisecond):
	}

	// deletion occurred in a separate workspace, we should not observe it
	watcher.GroupMembershipChanged("root|ws-01", sets.NewString("alice"), sets.String{})
	select {
	case event := <-watcher.ResultChan():
		t.Fatalf("unexpected event %v", event)
	case <-time.After(100 * time.Millisecond):
	}

	// deletion occurred in selected workspace, we should observe it
	watcher.GroupMembershipChanged("root|ws-03", sets.NewString("alice"), sets.String{})
	select {
	case event := <-watcher.ResultChan():
		if event.Type != watch.Deleted {
			t.Errorf("expected Deleted, got %v", event)
		}
		if logicalcluster.From(event.Object.(*workspaceapiv1beta1.Workspace)).String() != "root" {
			t.Errorf("expected %v, got %#v", "root", event.Object)
		}
		if event.Object.(*workspaceapiv1beta1.Workspace).Name != "ws-03" {
			t.Errorf("expected %v, got %#v", "ws-03", event.Object)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("timeout")
	}
}

func TestAddModifyDeleteEventsByGroup(t *testing.T) {
	watcher, _ := newTestWatcher("bob", []string{"group-one"}, matchAllPredicate(), newClusterWorkspaces("root:ws-01")...)
	go watcher.Watch()

	watcher.GroupMembershipChanged("root|ws-01", sets.String{}, sets.NewString("group-one"))
	select {
	case event := <-watcher.ResultChan():
		if event.Type != watch.Added {
			t.Errorf("expected added, got %v", event)
		}
		if logicalcluster.From(event.Object.(*workspaceapiv1beta1.Workspace)).String() != "root" {
			t.Errorf("expected %v, got %#v", "root", event.Object)
		}
		if event.Object.(*workspaceapiv1beta1.Workspace).Name != "ws-01" {
			t.Errorf("expected %v, got %#v", "ws-01", event.Object)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("timeout")
	}

	// the object didn't change, we shouldn't observe it
	watcher.GroupMembershipChanged("root|ws-01", sets.String{}, sets.NewString("group-one"))
	select {
	case event := <-watcher.ResultChan():
		t.Fatalf("unexpected event %v", event)
	case <-time.After(100 * time.Millisecond):
	}

	watcher.GroupMembershipChanged("root|ws-01", sets.String{}, sets.NewString("group-two"))
	select {
	case event := <-watcher.ResultChan():
		if event.Type != watch.Deleted {
			t.Errorf("expected Deleted, got %v", event)
		}
		if logicalcluster.From(event.Object.(*workspaceapiv1beta1.Workspace)).String() != "root" {
			t.Errorf("expected %v, got %#v", "root", event.Object)
		}
		if event.Object.(*workspaceapiv1beta1.Workspace).Name != "ws-01" {
			t.Errorf("expected %v, got %#v", "ws-01", event.Object)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("timeout")
	}
}

func newClusterWorkspaces(names ...string) []*tenancyv1alpha1.ClusterWorkspace {
	ret := []*tenancyv1alpha1.ClusterWorkspace{}
	for _, name := range names {
		parent, workspaceName := logicalcluster.New(name).Split()
		ret = append(ret, &tenancyv1alpha1.ClusterWorkspace{
			ObjectMeta: metav1.ObjectMeta{
				Name:        workspaceName,
				Annotations: map[string]string{logicalcluster.AnnotationKey: parent.String()},
			},
		})
	}

	return ret
}

func matchAllPredicate() storage.SelectionPredicate {
	return workspaceutil.MatchWorkspace(labels.Everything(), fields.Everything())
}
