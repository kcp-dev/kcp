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

package auth

import (
	"fmt"
	"strconv"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/kubernetes/pkg/controller"

	workspaceapi "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1fake "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/fake"
	tenancyInformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	workspacelisters "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
)

// mockReview implements the Review interface for test cases
type mockReview struct {
	users  []string
	groups []string
	err    string
}

// Users returns the users that can access a resource
func (r *mockReview) Users() []string {
	return r.users
}

// Groups returns the groups that can access a resource
func (r *mockReview) Groups() []string {
	return r.groups
}

func (r *mockReview) EvaluationError() string {
	return r.err
}

// common test users
var (
	alice = &user.DefaultInfo{
		Name:   "Alice",
		UID:    "alice-uid",
		Groups: []string{},
	}
	bob = &user.DefaultInfo{
		Name:   "Bob",
		UID:    "bob-uid",
		Groups: []string{"employee"},
	}
	eve = &user.DefaultInfo{
		Name:   "Eve",
		UID:    "eve-uid",
		Groups: []string{"employee"},
	}
	frank = &user.DefaultInfo{
		Name:   "Frank",
		UID:    "frank-uid",
		Groups: []string{},
	}
)

// mockReviewer returns the specified values for each supplied resource
type mockReviewer struct {
	expectedResults map[string]*mockReview
}

// Review returns the mapped review from the mock object, or an error if none exists
func (mr *mockReviewer) Review(name string) (Review, error) {
	review := mr.expectedResults[name]
	if review == nil {
		return nil, fmt.Errorf("Item %s does not exist", name)
	}
	return review, nil
}

func validateList(t *testing.T, lister Lister, user user.Info, expectedSet sets.String) {
	workspaceList, err := lister.List(user, labels.Everything())
	if err != nil {
		t.Errorf("Unexpected error %v", err)
	}
	results := sets.String{}
	for _, workspace := range workspaceList.Items {
		results.Insert(workspace.Name)
	}
	if results.Len() != expectedSet.Len() || !results.HasAll(expectedSet.List()...) {
		t.Errorf("User %v, Expected: %v, Actual: %v", user.GetName(), expectedSet, results)
	}
}

func TestSyncWorkspace(t *testing.T) {
	workspaceList := workspaceapi.ClusterWorkspaceList{
		Items: []workspaceapi.ClusterWorkspace{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "foo", ResourceVersion: "1"},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "bar", ResourceVersion: "2"},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "car", ResourceVersion: "3"},
			},
		},
	}
	mockKCPClient := tenancyv1fake.NewSimpleClientset(&workspaceList)
	mockKubeClient := fake.NewSimpleClientset()

	reviewer := &mockReviewer{
		expectedResults: map[string]*mockReview{
			"foo": {
				users:  []string{alice.GetName(), bob.GetName()},
				groups: eve.GetGroups(),
			},
			"bar": {
				users:  []string{frank.GetName(), eve.GetName()},
				groups: []string{"random"},
			},
			"car": {
				users:  []string{},
				groups: []string{},
			},
		},
	}

	kubeInformers := informers.NewSharedInformerFactory(mockKubeClient, controller.NoResyncPeriodFunc())
	kcpInformers := tenancyInformers.NewSharedInformerFactory(mockKCPClient, controller.NoResyncPeriodFunc())
	wsIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	wsLister := workspacelisters.NewClusterWorkspaceLister(wsIndexer)

	authorizationCache := NewAuthorizationCache(
		wsLister,
		kcpInformers.Tenancy().V1alpha1().ClusterWorkspaces().Informer(),
		reviewer,
		kubeInformers.Rbac().V1(),
	)
	// we prime the data we need here since we are not running reflectors
	for i := range workspaceList.Items {
		_ = wsIndexer.Add(&workspaceList.Items[i])
	}

	// synchronize the cache
	authorizationCache.synchronize()

	validateList(t, authorizationCache, alice, sets.NewString("foo"))
	validateList(t, authorizationCache, bob, sets.NewString("foo"))
	validateList(t, authorizationCache, eve, sets.NewString("foo", "bar"))
	validateList(t, authorizationCache, frank, sets.NewString("bar"))

	// modify access rules
	reviewer.expectedResults["foo"].users = []string{bob.GetName()}
	reviewer.expectedResults["foo"].groups = []string{"random"}
	reviewer.expectedResults["bar"].users = []string{alice.GetName(), eve.GetName()}
	reviewer.expectedResults["bar"].groups = []string{"employee"}
	reviewer.expectedResults["car"].users = []string{bob.GetName(), eve.GetName()}
	reviewer.expectedResults["car"].groups = []string{"employee"}

	// modify resource version on each namespace to simulate a change had occurred to force cache refresh
	for i := range workspaceList.Items {
		workspace := workspaceList.Items[i]
		oldVersion, err := strconv.Atoi(workspace.ResourceVersion)
		if err != nil {
			t.Errorf("Bad test setup, resource versions should be numbered, %v", err)
		}
		newVersion := strconv.Itoa(oldVersion + 1)
		workspace.ResourceVersion = newVersion
		_ = wsIndexer.Add(&workspace)
	}

	// now refresh the cache (which is resource version aware)
	authorizationCache.synchronize()

	// make sure new rights hold
	validateList(t, authorizationCache, alice, sets.NewString("bar"))
	validateList(t, authorizationCache, bob, sets.NewString("foo", "bar", "car"))
	validateList(t, authorizationCache, eve, sets.NewString("bar", "car"))
	validateList(t, authorizationCache, frank, sets.NewString())
}
