/*
Copyright 2022 The kcp Authors.

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

package forwardingregistry_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver"
	"k8s.io/apiextensions-apiserver/pkg/crdserverscheme"
	"k8s.io/apiextensions-apiserver/pkg/registry/customresource"
	"k8s.io/apiextensions-apiserver/pkg/registry/customresource/tableconvertor"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/generic/registry"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpfakedynamic "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/dynamic/fake"
	kcptesting "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/forwardingregistry"
)

var noxusGVR = schema.GroupVersionResource{Group: "mygroup.example.com", Resource: "noxus", Version: "v1beta1"}

func newStorage(t *testing.T, clusterClient kcpdynamic.ClusterInterface, apiExportIdentityHash string, patchConflictRetryBackoff *wait.Backoff) (mainStorage, statusStorage, scaleStorage rest.Storage) {
	t.Helper()

	var identities forwardingregistry.IdentityHashesFunc
	if apiExportIdentityHash != "" {
		identities = func(context.Context) []string { return []string{apiExportIdentityHash} }
	}
	return newStorageWithIdentities(t, clusterClient, identities, patchConflictRetryBackoff)
}

func newStorageWithIdentities(t *testing.T, clusterClient kcpdynamic.ClusterInterface, identities forwardingregistry.IdentityHashesFunc, patchConflictRetryBackoff *wait.Backoff) (mainStorage, statusStorage, scaleStorage rest.Storage) {
	t.Helper()

	gvr := noxusGVR
	groupVersion := gvr.GroupVersion()

	parameterScheme := runtime.NewScheme()
	parameterScheme.AddUnversionedTypes(groupVersion,
		&metav1.ListOptions{},
		&metav1.GetOptions{},
		&metav1.DeleteOptions{},
	)

	typer := apiserver.UnstructuredObjectTyper{
		Delegate:          parameterScheme,
		UnstructuredTyper: crdserverscheme.NewUnstructuredObjectTyper(),
	}

	kind := groupVersion.WithKind("Noxu")
	listKind := groupVersion.WithKind("NoxuItemList")

	headers := []apiextensionsv1.CustomResourceColumnDefinition{
		{Name: "Age", Type: "date", JSONPath: ".metadata.creationTimestamp"},
		{Name: "Replicas", Type: "integer", JSONPath: ".spec.replicas"},
		{Name: "Missing", Type: "string", JSONPath: ".spec.missing"},
		{Name: "Invalid", Type: "integer", JSONPath: ".spec.string"},
		{Name: "String", Type: "string", JSONPath: ".spec.string"},
		{Name: "StringFloat64", Type: "string", JSONPath: ".spec.float64"},
		{Name: "StringInt64", Type: "string", JSONPath: ".spec.replicas"},
		{Name: "StringBool", Type: "string", JSONPath: ".spec.bool"},
		{Name: "Float64", Type: "number", JSONPath: ".spec.float64"},
		{Name: "Bool", Type: "boolean", JSONPath: ".spec.bool"},
	}
	table, _ := tableconvertor.New(headers)

	ctx, cancelFn := context.WithCancel(context.Background())
	t.Cleanup(cancelFn)

	return forwardingregistry.NewStorageWithIdentities(
		ctx,
		gvr,
		identities,
		kind,
		listKind,
		customresource.NewStrategy(
			typer,
			true,
			kind,
			forwardingregistry.ValidatePathSegmentName,
			nil,
			nil,
			nil,
			&apiextensions.CustomResourceSubresourceStatus{},
			nil,
			[]apiextensionsv1.SelectableField{},
		),
		nil,
		table,
		nil,
		func(ctx context.Context) (kcpdynamic.ClusterInterface, error) { return clusterClient, nil },
		patchConflictRetryBackoff,
		forwardingregistry.StorageWrapperFunc(func(_ schema.GroupResource, store *forwardingregistry.StoreFuncs) {
		}))
}

func createResource(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "mygroup.example.com/v1beta1",
			"kind":       "Noxu",
			"metadata": map[string]interface{}{
				"namespace":         namespace,
				"name":              name,
				"creationTimestamp": time.Now().Add(-time.Hour*12 - 30*time.Minute).UTC().Format(time.RFC3339),
				"annotations":       map[string]interface{}{logicalcluster.AnnotationKey: "test"},
			},
			"spec": map[string]interface{}{
				"replicas":         int64(7),
				"string":           "string",
				"float64":          3.1415926,
				"bool":             true,
				"stringList":       []interface{}{"foo", "bar"},
				"mixedList":        []interface{}{"foo", int64(42)},
				"nonPrimitiveList": []interface{}{"foo", []interface{}{int64(1), int64(2)}},
			},
		},
	}
}

func TestGet(t *testing.T) {
	t.Parallel()
	fakeClient := kcpfakedynamic.NewSimpleDynamicClient(runtime.NewScheme())
	storage, _, _ := newStorage(t, fakeClient, "", nil)
	ctx := request.WithNamespace(context.Background(), "default")
	ctx = request.WithCluster(ctx, request.Cluster{Name: "test"})

	getter := storage.(rest.Getter)
	_, err := getter.Get(ctx, "foo", &metav1.GetOptions{})
	require.EqualError(t, err, "noxus.mygroup.example.com \"foo\" not found")

	resource := createResource("default", "foo")
	_ = fakeClient.Tracker().Cluster(logicalcluster.NewPath("test")).Add(resource)

	result, err := getter.Get(ctx, "foo", &metav1.GetOptions{})
	require.NoError(t, err)
	require.Truef(t, apiequality.Semantic.DeepEqual(resource, result), "expected:\n%v\nactual:\n%v", resource, result)
}

func TestList(t *testing.T) {
	t.Parallel()
	resources := []runtime.Object{createResource("default", "foo"), createResource("default", "foo2")}
	fakeClient := kcpfakedynamic.NewSimpleDynamicClient(runtime.NewScheme(), resources...)
	storage, _, _ := newStorage(t, fakeClient, "", nil)
	ctx := request.WithNamespace(context.Background(), "default")
	ctx = request.WithCluster(ctx, request.Cluster{Name: "test"})

	lister := storage.(rest.Lister)
	result, err := lister.List(ctx, &internalversion.ListOptions{})
	require.NoError(t, err)
	require.IsType(t, &unstructured.UnstructuredList{}, result)
	resultResources := result.(*unstructured.UnstructuredList).Items
	require.Len(t, resultResources, len(resources))
	for i, resource := range resources {
		resource := *resource.(*unstructured.Unstructured)
		require.Truef(t, apiequality.Semantic.DeepEqual(resource, resultResources[i]), "expected:\n%v\nactual:\n%v", resource, resultResources[0])
	}
	require.Len(t, fakeClient.Actions(), 1)
	require.Equal(t, "noxus", fakeClient.Actions()[0].GetResource().Resource)
}

func TestWildcardListWithAPIExportIdentity(t *testing.T) {
	t.Parallel()
	resources := []runtime.Object{createResource("default", "foo"), createResource("default", "foo2")}
	noxusGVRWithHash := noxusGVR.GroupVersion().WithResource("noxus:" + "apiExportIdentityHash")
	fakeClient := kcpfakedynamic.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			noxusGVR:         "NoxuList",
			noxusGVRWithHash: "NoxuList",
		})
	for _, resource := range resources {
		_ = fakeClient.Tracker().Cluster(logicalcluster.NewPath("test")).Create(noxusGVRWithHash, resource, "default")
	}

	storage, _, _ := newStorage(t, fakeClient, "apiExportIdentityHash", nil)
	ctx := request.WithNamespace(context.Background(), "")
	ctx = request.WithCluster(ctx, request.Cluster{Wildcard: true})

	lister := storage.(rest.Lister)
	result, err := lister.List(ctx, &internalversion.ListOptions{})
	require.NoError(t, err)
	require.IsType(t, &unstructured.UnstructuredList{}, result)
	resultResources := result.(*unstructured.UnstructuredList).Items
	require.Len(t, resultResources, len(resources))
	for i, resource := range resources {
		resource := *resource.(*unstructured.Unstructured)
		require.Truef(t, apiequality.Semantic.DeepEqual(resource, resultResources[i]), "expected:\n%v\nactual:\n%v", resource, resultResources[0])
	}
	require.Len(t, fakeClient.Actions(), 1)
	require.Equal(t, "noxus:apiExportIdentityHash", fakeClient.Actions()[0].GetResource().Resource)
}

func checkWatchEvents(t *testing.T, addEvents func(), watchCall func() (watch.Interface, error), expectedEvents []watch.Event) {
	t.Helper()

	watchingStarted := make(chan bool, 1)
	go func() {
		<-watchingStarted
		addEvents()
	}()

	watcher, err := watchCall()
	require.NoError(t, err)

	watchingStarted <- true
	watcherChan := watcher.ResultChan()
	var event watch.Event

	for _, expectedEvent := range expectedEvents {
		select {
		case event = <-watcherChan:
		case <-time.After(wait.ForeverTestTimeout):
			require.Fail(t, "Watch event not received")
		}
		require.Equal(t, expectedEvent.Type, event.Type, "Event type is wrong")
		require.True(t, apiequality.Semantic.DeepEqual(expectedEvent.Object, event.Object), cmp.Diff(expectedEvent.Object, event.Object))
	}
}

func TestWatch(t *testing.T) {
	t.Parallel()
	resources := []runtime.Object{createResource("default", "foo"), createResource("default", "foo2")}
	fakeClient := kcpfakedynamic.NewSimpleDynamicClient(runtime.NewScheme())
	fakeWatcher := watch.NewFake()
	t.Cleanup(fakeWatcher.Stop)
	fakeClient.PrependWatchReactor("noxus", kcptesting.DefaultWatchReactor(fakeWatcher, nil))
	storage, _, _ := newStorage(t, fakeClient, "", nil)
	ctx := request.WithNamespace(context.Background(), "default")
	ctx = request.WithCluster(ctx, request.Cluster{Name: "test"})

	watchedError := &metav1.Status{
		Status:  "Failure",
		Message: "message",
	}

	checkWatchEvents(t,
		func() {
			fakeWatcher.Add(resources[0])
			fakeWatcher.Add(resources[1])
			fakeWatcher.Modify(resources[0])
			fakeWatcher.Delete(resources[1])
			fakeWatcher.Error(watchedError)
		},
		func() (watch.Interface, error) {
			watcher := storage.(rest.Watcher)
			return watcher.Watch(ctx, &internalversion.ListOptions{})
		}, []watch.Event{
			{Type: watch.Added, Object: resources[0]},
			{Type: watch.Added, Object: resources[1]},
			{Type: watch.Modified, Object: resources[0]},
			{Type: watch.Deleted, Object: resources[1]},
			{Type: watch.Error, Object: watchedError},
		})

	require.Len(t, fakeClient.Actions(), 1)
	require.Equal(t, "noxus", fakeClient.Actions()[0].GetResource().Resource)
}

func TestWildcardWatchWithPIExportIdentity(t *testing.T) {
	t.Parallel()
	resources := []runtime.Object{createResource("default", "foo"), createResource("default", "foo2")}
	noxusGVRWithHash := noxusGVR.GroupVersion().WithResource("noxus:apiExportIdentityHash")
	fakeClient := kcpfakedynamic.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			noxusGVR:         "NoxuList",
			noxusGVRWithHash: "NoxuList",
		})
	fakeWatcher := watch.NewFake()
	t.Cleanup(fakeWatcher.Stop)
	fakeClient.PrependWatchReactor("noxus:apiExportIdentityHash", kcptesting.DefaultWatchReactor(fakeWatcher, nil))
	storage, _, _ := newStorage(t, fakeClient, "apiExportIdentityHash", nil)
	ctx := request.WithNamespace(context.Background(), "")
	ctx = request.WithCluster(ctx, request.Cluster{Wildcard: true})

	watchedError := &metav1.Status{
		Status:  "Failure",
		Message: "message",
	}

	checkWatchEvents(t,
		func() {
			fakeWatcher.Add(resources[0])
			fakeWatcher.Add(resources[1])
			fakeWatcher.Modify(resources[0])
			fakeWatcher.Delete(resources[1])
			fakeWatcher.Error(watchedError)
		},
		func() (watch.Interface, error) {
			watcher := storage.(rest.Watcher)
			return watcher.Watch(ctx, &internalversion.ListOptions{})
		}, []watch.Event{
			{Type: watch.Added, Object: resources[0]},
			{Type: watch.Added, Object: resources[1]},
			{Type: watch.Modified, Object: resources[0]},
			{Type: watch.Deleted, Object: resources[1]},
			{Type: watch.Error, Object: watchedError},
		})

	require.Len(t, fakeClient.Actions(), 1)
	require.Equal(t, "noxus:apiExportIdentityHash", fakeClient.Actions()[0].GetResource().Resource)
}

// identityListReactor answers a list on the given resource with the given
// items and list resourceVersion.
func identityListReactor(resourceVersion string, items ...runtime.Object) kcptesting.ReactionFunc {
	return func(action kcptesting.Action) (bool, runtime.Object, error) {
		list := &unstructured.UnstructuredList{}
		list.SetResourceVersion(resourceVersion)
		for _, item := range items {
			list.Items = append(list.Items, *item.(*unstructured.Unstructured).DeepCopy())
		}
		return true, list, nil
	}
}

func newMultiIdentityFakeClient(t *testing.T) *kcpfakedynamic.FakeDynamicClusterClientset {
	t.Helper()
	return kcpfakedynamic.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			noxusGVR: "NoxuList",
			noxusGVR.GroupVersion().WithResource("noxus:hash1"): "NoxuList",
			noxusGVR.GroupVersion().WithResource("noxus:hash2"): "NoxuList",
		})
}

func listedResources(fakeClient *kcpfakedynamic.FakeDynamicClusterClientset) []string {
	actions := fakeClient.Actions()
	resources := make([]string, 0, len(actions))
	for _, action := range actions {
		resources = append(resources, action.GetResource().Resource)
	}
	return resources
}

func TestWildcardListWithMultipleIdentities(t *testing.T) {
	t.Parallel()
	hash1Resources := []runtime.Object{createResource("default", "foo"), createResource("default", "foo2")}
	hash2Resources := []runtime.Object{createResource("default", "bar")}
	fakeClient := newMultiIdentityFakeClient(t)
	fakeClient.PrependReactor("list", "noxus:hash1", identityListReactor("100", hash1Resources...))
	fakeClient.PrependReactor("list", "noxus:hash2", identityListReactor("250", hash2Resources...))

	storage, _, _ := newStorageWithIdentities(t, fakeClient, func(context.Context) []string { return []string{"hash1", "hash2"} }, nil)
	ctx := request.WithNamespace(context.Background(), "")
	ctx = request.WithCluster(ctx, request.Cluster{Wildcard: true})
	lister := storage.(rest.Lister)

	expected := append(append([]runtime.Object{}, hash1Resources...), hash2Resources...)
	for _, options := range []*internalversion.ListOptions{
		{},
		{Limit: 1},
		{Continue: "some-token"},
	} {
		fakeClient.ClearActions()
		result, err := lister.List(ctx, options)
		require.NoError(t, err)
		require.IsType(t, &unstructured.UnstructuredList{}, result)
		list := result.(*unstructured.UnstructuredList)
		require.Len(t, list.Items, len(expected), "options %+v", options)
		for i, resource := range expected {
			resource := *resource.(*unstructured.Unstructured)
			require.Truef(t, apiequality.Semantic.DeepEqual(resource, list.Items[i]), "expected:\n%v\nactual:\n%v", resource, list.Items[i])
		}
		require.Equal(t, "250", list.GetResourceVersion(), "should return the largest resourceVersion across identities")
		require.Empty(t, list.GetContinue(), "should not paginate across identities")
		require.Nil(t, list.GetRemainingItemCount())
		require.Equal(t, []string{"noxus:hash1", "noxus:hash2"}, listedResources(fakeClient))
	}
}

func TestWildcardListWithNoIdentities(t *testing.T) {
	t.Parallel()
	fakeClient := newMultiIdentityFakeClient(t)
	storage, _, _ := newStorageWithIdentities(t, fakeClient, func(context.Context) []string { return nil }, nil)
	ctx := request.WithNamespace(context.Background(), "")
	ctx = request.WithCluster(ctx, request.Cluster{Wildcard: true})

	result, err := storage.(rest.Lister).List(ctx, &internalversion.ListOptions{})
	require.NoError(t, err)
	require.IsType(t, &unstructured.UnstructuredList{}, result)
	require.Empty(t, result.(*unstructured.UnstructuredList).Items)
	require.Empty(t, fakeClient.Actions(), "should not call the shard when no identity is served")
}

func TestWildcardListWithOneIdentityNotFound(t *testing.T) {
	t.Parallel()
	hash1Resources := []runtime.Object{createResource("default", "foo"), createResource("default", "foo2")}
	fakeClient := newMultiIdentityFakeClient(t)
	fakeClient.PrependReactor("list", "noxus:hash1", identityListReactor("100", hash1Resources...))
	fakeClient.PrependReactor("list", "noxus:hash2", func(action kcptesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.NewNotFound(action.GetResource().GroupResource(), "")
	})

	storage, _, _ := newStorageWithIdentities(t, fakeClient, func(context.Context) []string { return []string{"hash1", "hash2"} }, nil)
	ctx := request.WithNamespace(context.Background(), "")
	ctx = request.WithCluster(ctx, request.Cluster{Wildcard: true})

	result, err := storage.(rest.Lister).List(ctx, &internalversion.ListOptions{})
	require.NoError(t, err)
	require.IsType(t, &unstructured.UnstructuredList{}, result)
	list := result.(*unstructured.UnstructuredList)
	require.Len(t, list.Items, len(hash1Resources))
	for i, resource := range hash1Resources {
		resource := *resource.(*unstructured.Unstructured)
		require.Truef(t, apiequality.Semantic.DeepEqual(resource, list.Items[i]), "expected:\n%v\nactual:\n%v", resource, list.Items[i])
	}
	require.Equal(t, "100", list.GetResourceVersion())
	require.Equal(t, []string{"noxus:hash1", "noxus:hash2"}, listedResources(fakeClient))
}

func TestWildcardWatchWithMultipleIdentities(t *testing.T) {
	t.Parallel()
	resources := []runtime.Object{createResource("default", "foo"), createResource("default", "bar")}
	fakeClient := newMultiIdentityFakeClient(t)
	fakeWatcher1 := watch.NewFake()
	t.Cleanup(fakeWatcher1.Stop)
	fakeWatcher2 := watch.NewFake()
	t.Cleanup(fakeWatcher2.Stop)
	fakeClient.PrependWatchReactor("noxus:hash1", kcptesting.DefaultWatchReactor(fakeWatcher1, nil))
	fakeClient.PrependWatchReactor("noxus:hash2", kcptesting.DefaultWatchReactor(fakeWatcher2, nil))

	storage, _, _ := newStorageWithIdentities(t, fakeClient, func(context.Context) []string { return []string{"hash1", "hash2"} }, nil)
	ctx := request.WithNamespace(context.Background(), "")
	ctx = request.WithCluster(ctx, request.Cluster{Wildcard: true})

	watcher, err := storage.(rest.Watcher).Watch(ctx, &internalversion.ListOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"noxus:hash1", "noxus:hash2"}, listedResources(fakeClient))

	receive := func() watch.Event {
		t.Helper()
		select {
		case event, ok := <-watcher.ResultChan():
			require.True(t, ok, "result channel closed unexpectedly")
			return event
		case <-time.After(wait.ForeverTestTimeout):
			require.Fail(t, "Watch event not received")
			return watch.Event{}
		}
	}

	// Events from either source come out of the single merged watch.
	fakeWatcher1.Add(resources[0])
	event := receive()
	require.Equal(t, watch.Added, event.Type)
	require.True(t, apiequality.Semantic.DeepEqual(resources[0], event.Object), cmp.Diff(resources[0], event.Object))

	fakeWatcher2.Add(resources[1])
	event = receive()
	require.Equal(t, watch.Added, event.Type)
	require.True(t, apiequality.Semantic.DeepEqual(resources[1], event.Object), cmp.Diff(resources[1], event.Object))

	fakeWatcher1.Modify(resources[0])
	fakeWatcher2.Delete(resources[1])
	types := []watch.EventType{receive().Type, receive().Type}
	require.ElementsMatch(t, []watch.EventType{watch.Modified, watch.Deleted}, types)

	// Stop terminates both sources and closes the merged channel exactly once.
	watcher.Stop()
	watcher.Stop()
	require.True(t, fakeWatcher1.IsStopped())
	require.True(t, fakeWatcher2.IsStopped())
	select {
	case _, ok := <-watcher.ResultChan():
		require.False(t, ok, "result channel should be closed after Stop")
	case <-time.After(wait.ForeverTestTimeout):
		require.Fail(t, "result channel not closed after Stop")
	}
}

func TestWildcardWatchStopsWhenContextIsDone(t *testing.T) {
	t.Parallel()
	fakeClient := newMultiIdentityFakeClient(t)
	fakeWatcher1 := watch.NewFake()
	t.Cleanup(fakeWatcher1.Stop)
	fakeWatcher2 := watch.NewFake()
	t.Cleanup(fakeWatcher2.Stop)
	fakeClient.PrependWatchReactor("noxus:hash1", kcptesting.DefaultWatchReactor(fakeWatcher1, nil))
	fakeClient.PrependWatchReactor("noxus:hash2", kcptesting.DefaultWatchReactor(fakeWatcher2, nil))

	storage, _, _ := newStorageWithIdentities(t, fakeClient, func(context.Context) []string { return []string{"hash1", "hash2"} }, nil)
	ctx, cancel := context.WithCancel(context.Background())
	ctx = request.WithNamespace(ctx, "")
	ctx = request.WithCluster(ctx, request.Cluster{Wildcard: true})

	watcher, err := storage.(rest.Watcher).Watch(ctx, &internalversion.ListOptions{})
	require.NoError(t, err)

	cancel()
	select {
	case _, ok := <-watcher.ResultChan():
		require.False(t, ok, "result channel should be closed once the context is done")
	case <-time.After(wait.ForeverTestTimeout):
		require.Fail(t, "result channel not closed after context cancellation")
	}
	require.True(t, fakeWatcher1.IsStopped())
	require.True(t, fakeWatcher2.IsStopped())
}

func TestGetWithIdentities(t *testing.T) {
	t.Parallel()
	noxusGVRWithHash1 := noxusGVR.GroupVersion().WithResource("noxus:hash1")
	fakeClient := newMultiIdentityFakeClient(t)
	resource := createResource("default", "foo")
	_ = fakeClient.Tracker().Cluster(logicalcluster.NewPath("test")).Add(resource)
	_ = fakeClient.Tracker().Cluster(logicalcluster.NewPath("test")).Create(noxusGVRWithHash1, resource, "default")

	ctx := request.WithNamespace(context.Background(), "default")
	ctx = request.WithCluster(ctx, request.Cluster{Name: "test"})

	// Several identities: forward without a suffix and let the shard resolve
	// the identity from the binding in the target cluster.
	storage, _, _ := newStorageWithIdentities(t, fakeClient, func(context.Context) []string { return []string{"hash1", "hash2"} }, nil)
	result, err := storage.(rest.Getter).Get(ctx, "foo", &metav1.GetOptions{})
	require.NoError(t, err)
	require.Truef(t, apiequality.Semantic.DeepEqual(resource, result), "expected:\n%v\nactual:\n%v", resource, result)
	require.Equal(t, []string{"noxus"}, listedResources(fakeClient))

	// No identity at all: same.
	fakeClient.ClearActions()
	storage, _, _ = newStorageWithIdentities(t, fakeClient, func(context.Context) []string { return nil }, nil)
	_, err = storage.(rest.Getter).Get(ctx, "foo", &metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"noxus"}, listedResources(fakeClient))

	// Exactly one identity: the suffix is appended as before.
	fakeClient.ClearActions()
	storage, _, _ = newStorageWithIdentities(t, fakeClient, func(context.Context) []string { return []string{"hash1"} }, nil)
	result, err = storage.(rest.Getter).Get(ctx, "foo", &metav1.GetOptions{})
	require.NoError(t, err)
	require.Truef(t, apiequality.Semantic.DeepEqual(resource, result), "expected:\n%v\nactual:\n%v", resource, result)
	require.Equal(t, []string{"noxus:hash1"}, listedResources(fakeClient))
}

func updateReactor(fakeClient *kcpfakedynamic.FakeDynamicClusterClientset) kcptesting.ReactionFunc {
	return func(action kcptesting.Action) (handled bool, ret runtime.Object, err error) {
		updateAction := action.(kcptesting.UpdateAction)
		actionResource := updateAction.GetObject().(*unstructured.Unstructured)

		existingObject, err := fakeClient.Tracker().Cluster(logicalcluster.NewPath("test")).Get(action.GetResource(), action.GetNamespace(), actionResource.GetName())
		if err != nil {
			return true, nil, err
		}
		existingResource := existingObject.(*unstructured.Unstructured)

		if existingResource.GetResourceVersion() != actionResource.GetResourceVersion() {
			return true, nil, errors.NewConflict(action.GetResource().GroupResource(), existingResource.GetName(), fmt.Errorf(registry.OptimisticLockErrorMsg))
		}
		if err := fakeClient.Tracker().Cluster(logicalcluster.NewPath("test")).Update(action.GetResource(), actionResource, action.GetNamespace()); err != nil {
			return true, nil, err
		}

		return true, actionResource, nil
	}
}

func TestUpdate(t *testing.T) {
	t.Parallel()
	resource := createResource("default", "foo")
	resource.SetGeneration(1)
	resource.SetResourceVersion("100")
	fakeClient := kcpfakedynamic.NewSimpleDynamicClient(runtime.NewScheme())
	fakeClient.PrependReactor("update", "noxus", updateReactor(fakeClient))

	storage, _, _ := newStorage(t, fakeClient, "", nil)
	ctx := request.WithNamespace(context.Background(), "default")
	ctx = request.WithCluster(ctx, request.Cluster{Name: "test"})
	updated := resource.DeepCopy()

	newReplicas, _, err := unstructured.NestedInt64(updated.UnstructuredContent(), "spec", "replicas")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	newReplicas++
	_ = unstructured.SetNestedField(updated.UnstructuredContent(), newReplicas, "spec", "replicas")

	updater := storage.(rest.Updater)
	_, _, err = updater.Update(ctx, updated.GetName(), rest.DefaultUpdatedObjectInfo(updated), rest.ValidateAllObjectFunc, rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{})
	require.EqualError(t, err, "noxus.mygroup.example.com \"foo\" not found")

	_ = fakeClient.Tracker().Cluster(logicalcluster.NewPath("test")).Add(resource)
	result, _, err := updater.Update(ctx, updated.GetName(), rest.DefaultUpdatedObjectInfo(updated), rest.ValidateAllObjectFunc, rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{})
	require.NoError(t, err)

	// Now we can check that the object has been updated
	require.True(t, apiequality.Semantic.DeepEqual(updated, result), "expected:\n%V\nactual:\n%V", updated, result)

	fakeClient.ClearActions()
	updated.SetResourceVersion("101")
	newReplicas++
	_ = unstructured.SetNestedField(updated.UnstructuredContent(), newReplicas, "spec", "replicas")
	_, _, err = updater.Update(ctx, updated.GetName(), rest.DefaultUpdatedObjectInfo(updated), rest.ValidateAllObjectFunc, rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{})
	require.EqualError(t, err, "Operation cannot be fulfilled on noxus.mygroup.example.com \"foo\": the object has been modified; please apply your changes to the latest version and try again")

	updates := 0
	for _, action := range fakeClient.Actions() {
		if action.GetVerb() == "update" {
			updates++
		}
	}
	require.Equalf(t, 1, updates, "Should not have retried calling client.Update in case of conflict: it's an Update call.")
}

func TestUpdateWithForceAllowCreate(t *testing.T) {
	t.Parallel()
	resource := createResource("default", "foo")
	resource.SetGeneration(1)
	resource.SetResourceVersion("100")
	fakeClient := kcpfakedynamic.NewSimpleDynamicClient(runtime.NewScheme())
	fakeClient.PrependReactor("update", "noxus", updateReactor(fakeClient))

	storage, _, _ := newStorage(t, fakeClient, "", nil)
	ctx := request.WithNamespace(context.Background(), "default")
	ctx = request.WithCluster(ctx, request.Cluster{Name: "test"})
	updated := resource.DeepCopy()

	newReplicas, _, err := unstructured.NestedInt64(updated.UnstructuredContent(), "spec", "replicas")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	newReplicas++
	_ = unstructured.SetNestedField(updated.UnstructuredContent(), newReplicas, "spec", "replicas")

	updater := storage.(rest.Updater)
	result, _, err := updater.Update(ctx, updated.GetName(), rest.DefaultUpdatedObjectInfo(updated), rest.ValidateAllObjectFunc, rest.ValidateAllObjectUpdateFunc, true, &metav1.UpdateOptions{})
	require.NoError(t, err)

	// Now we can check that the object has been updated
	require.True(t, apiequality.Semantic.DeepEqual(updated, result), "expected:\n%V\nactual:\n%V", updated, result)

	fakeClient.ClearActions()
	updated.SetResourceVersion("101")
	newReplicas++
	_ = unstructured.SetNestedField(updated.UnstructuredContent(), newReplicas, "spec", "replicas")
	_, _, err = updater.Update(ctx, updated.GetName(), rest.DefaultUpdatedObjectInfo(updated), rest.ValidateAllObjectFunc, rest.ValidateAllObjectUpdateFunc, true, &metav1.UpdateOptions{})
	require.EqualError(t, err, "Operation cannot be fulfilled on noxus.mygroup.example.com \"foo\": the object has been modified; please apply your changes to the latest version and try again")

	updates := 0
	for _, action := range fakeClient.Actions() {
		if action.GetVerb() == "update" {
			updates++
		}
	}
	require.Equalf(t, 1, updates, "Should not have retried calling client.Update in case of conflict: it's an Update call.")
}

func TestStatusUpdate(t *testing.T) {
	t.Parallel()
	resource := createResource("default", "foo")
	resource.SetGeneration(1)
	resource.SetResourceVersion("100")
	fakeClient := kcpfakedynamic.NewSimpleDynamicClient(runtime.NewScheme(), resource)
	fakeClient.PrependReactor("update", "noxus", updateReactor(fakeClient))

	_, statusStorage, _ := newStorage(t, fakeClient, "", nil)
	ctx := request.WithNamespace(context.Background(), "default")
	ctx = request.WithCluster(ctx, request.Cluster{Name: "test"})
	statusUpdated := resource.DeepCopy()
	if err := unstructured.SetNestedField(statusUpdated.UnstructuredContent(), int64(10), "status", "availableReplicas"); err != nil {
		require.NoError(t, err)
	}

	updater := statusStorage.(rest.Updater)
	result, _, err := updater.Update(ctx, statusUpdated.GetName(), rest.DefaultUpdatedObjectInfo(statusUpdated), rest.ValidateAllObjectFunc, rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{})
	resultResource := result.(*unstructured.Unstructured)
	require.NoError(t, err)
	updatedGeneration, _, err := unstructured.NestedInt64(resultResource.UnstructuredContent(), "metadata", "generation")
	require.NoError(t, err)
	require.Equalf(t, int64(1), updatedGeneration, "Generation should not be incremented when updating the Status")

	// We check that the status has been updated
	require.True(t, apiequality.Semantic.DeepEqual(statusUpdated, result), "expected:\n%V\nactual:\n%V", statusUpdated, result)
}

func TestPatch(t *testing.T) {
	t.Parallel()
	resource := createResource("default", "foo")
	resource.SetGeneration(1)
	resource.SetResourceVersion("100")
	fakeClient := kcpfakedynamic.NewSimpleDynamicClient(runtime.NewScheme())
	fakeClient.PrependReactor("update", "noxus", updateReactor(fakeClient))

	backoff := retry.DefaultRetry
	backoff.Steps = 5
	storage, _, _ := newStorage(t, fakeClient, "", &backoff)
	ctx := request.WithNamespace(context.Background(), "default")
	ctx = request.WithRequestInfo(ctx, &request.RequestInfo{Verb: "patch"})
	ctx = request.WithCluster(ctx, request.Cluster{Name: "test"})

	patcher := func(ctx context.Context, newObj, oldObj runtime.Object) (runtime.Object, error) {
		if reflect.DeepEqual(&unstructured.Unstructured{}, oldObj.(*unstructured.Unstructured)) {
			return nil, errors.NewNotFound(schema.ParseGroupResource("noxus.mygroup.example.com"), "foo")
		}
		updated := oldObj.DeepCopyObject().(*unstructured.Unstructured)
		newReplicas, _, err := unstructured.NestedInt64(updated.UnstructuredContent(), "spec", "replicas")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		newReplicas++
		_ = unstructured.SetNestedField(updated.UnstructuredContent(), newReplicas, "spec", "replicas")
		return updated, nil
	}

	updater := storage.(rest.Updater)
	_, _, err := updater.Update(ctx, resource.GetName(), rest.DefaultUpdatedObjectInfo(nil, patcher), rest.ValidateAllObjectFunc, rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{})
	require.EqualError(t, err, "noxus.mygroup.example.com \"foo\" not found")

	_ = fakeClient.Tracker().Cluster(logicalcluster.NewPath("test")).Add(resource)
	getCallCounts := 0
	noMoreConflicts := 4
	fakeClient.PrependReactor("get", "noxus", func(action kcptesting.Action) (handled bool, ret runtime.Object, err error) {
		getCallCounts++
		if getCallCounts < noMoreConflicts {
			withChangedResourceVersion := resource.DeepCopy()
			withChangedResourceVersion.SetResourceVersion("50")
			return true, withChangedResourceVersion, nil
		}
		return true, resource, nil
	})

	resultObj, _, err := updater.Update(ctx, resource.GetName(), rest.DefaultUpdatedObjectInfo(nil, patcher), rest.ValidateAllObjectFunc, rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{})
	require.NoError(t, err)
	updates := 0
	for _, action := range fakeClient.Actions() {
		if action.GetVerb() == "update" {
			updates++
		}
	}
	require.Equalf(t, noMoreConflicts, updates, "Should have tried calling client.Update %d times to overcome resourceVersion conflicts.", noMoreConflicts)

	expectedObj, _ := patcher(ctx, nil, resource)
	expected := expectedObj.(*unstructured.Unstructured)
	result := resultObj.(*unstructured.Unstructured)

	require.True(t, apiequality.Semantic.DeepEqual(expected, result), "expected:\n%V\nactual:\n%V", expected, result)

	getCallCounts = 0
	noMoreConflicts = backoff.Steps + 1
	fakeClient.ClearActions()

	_, _, err = updater.Update(ctx, resource.GetName(), rest.DefaultUpdatedObjectInfo(nil, patcher), rest.ValidateAllObjectFunc, rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{})
	require.EqualError(t, err, "Operation cannot be fulfilled on noxus.mygroup.example.com \"foo\": the object has been modified; please apply your changes to the latest version and try again")

	updates = 0
	for _, action := range fakeClient.Actions() {
		if action.GetVerb() == "update" {
			updates++
		}
	}
	require.Equalf(t, backoff.Steps, updates, "Should have tried calling client.Update %d times to overcome resourceVersion conflicts, before finally returning a Conflict error.", backoff.Steps)
}

func TestWatchNotFoundOnShard(t *testing.T) {
	t.Parallel()

	// The delegate answers NotFound: this shard does not serve the resource,
	// which happens for a claimed resource whose APIExport nothing has bound
	// here yet.
	notFound := func(kcptesting.Action) (bool, watch.Interface, error) {
		return true, nil, errors.NewNotFound(noxusGVR.GroupResource(), "")
	}

	for _, tc := range []struct {
		name              string
		sendInitialEvents *bool
		wantErr           bool
	}{
		{
			// A plain watch gets an empty, open watch rather than a 404, so a
			// wildcard consumer aggregating across shards is not wedged by a
			// shard that simply holds none of these objects.
			name:              "plain watch is served an empty watch",
			sendInitialEvents: nil,
			wantErr:           false,
		},
		{
			// A WatchList client treats the stream as its initial list and waits
			// for an "initial-events-end" bookmark before it considers itself
			// synced. An empty watch never sends one and never errors, so the
			// client would wait forever; the error is what makes it retry.
			name:              "watchlist is given the error to retry on",
			sendInitialEvents: ptr.To(true),
			wantErr:           true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fakeClient := kcpfakedynamic.NewSimpleDynamicClient(runtime.NewScheme())
			fakeClient.PrependWatchReactor("noxus", notFound)
			storage, _, _ := newStorage(t, fakeClient, "", nil)
			ctx := request.WithNamespace(context.Background(), "default")
			ctx = request.WithCluster(ctx, request.Cluster{Name: "test"})

			w, err := storage.(rest.Watcher).Watch(ctx, &internalversion.ListOptions{
				SendInitialEvents: tc.sendInitialEvents,
			})
			if tc.wantErr {
				require.True(t, errors.IsNotFound(err), "expected a NotFound, got %v", err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, w)
			t.Cleanup(w.Stop)
			select {
			case ev, ok := <-w.ResultChan():
				require.False(t, ok, "expected no events, got %v", ev)
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}

// initialEventsEndBookmark is the bookmark a server sends to close a
// WatchList's initial list.
func initialEventsEndBookmark() *unstructured.Unstructured {
	obj := createResource("", "")
	obj.SetAnnotations(map[string]string{metav1.InitialEventsAnnotationKey: "true"})
	return obj
}

// TestWildcardWatchListWithMultipleIdentities asserts that a WatchList across
// several identities emits exactly one "initial-events-end" bookmark, and only
// after every identity has finished its initial list. Forwarding the first
// source's bookmark would tell the client it is synced while another identity
// is still sending initial events.
func TestWildcardWatchListWithMultipleIdentities(t *testing.T) {
	t.Parallel()
	fakeClient := newMultiIdentityFakeClient(t)
	fakeWatcher1 := watch.NewFake()
	t.Cleanup(fakeWatcher1.Stop)
	fakeWatcher2 := watch.NewFake()
	t.Cleanup(fakeWatcher2.Stop)
	fakeClient.PrependWatchReactor("noxus:hash1", kcptesting.DefaultWatchReactor(fakeWatcher1, nil))
	fakeClient.PrependWatchReactor("noxus:hash2", kcptesting.DefaultWatchReactor(fakeWatcher2, nil))

	storage, _, _ := newStorageWithIdentities(t, fakeClient, func(context.Context) []string { return []string{"hash1", "hash2"} }, nil)
	ctx := request.WithNamespace(context.Background(), "")
	ctx = request.WithCluster(ctx, request.Cluster{Wildcard: true})

	watcher, err := storage.(rest.Watcher).Watch(ctx, &internalversion.ListOptions{SendInitialEvents: ptr.To(true)})
	require.NoError(t, err)
	t.Cleanup(watcher.Stop)

	receive := func() watch.Event {
		t.Helper()
		select {
		case event, ok := <-watcher.ResultChan():
			require.True(t, ok, "result channel closed unexpectedly")
			return event
		case <-time.After(wait.ForeverTestTimeout):
			require.Fail(t, "Watch event not received")
			return watch.Event{}
		}
	}
	requireNothingYet := func() {
		t.Helper()
		select {
		case event := <-watcher.ResultChan():
			require.Failf(t, "unexpected event", "got %v before every identity finished its initial list", event)
		case <-time.After(100 * time.Millisecond):
		}
	}

	// The first source closing its initial list must not reach the client.
	fakeWatcher1.Action(watch.Bookmark, initialEventsEndBookmark())
	requireNothingYet()

	// The last one does, exactly once.
	fakeWatcher2.Action(watch.Bookmark, initialEventsEndBookmark())
	event := receive()
	require.Equal(t, watch.Bookmark, event.Type)
	accessor, err := meta.Accessor(event.Object)
	require.NoError(t, err)
	require.Contains(t, accessor.GetAnnotations(), metav1.InitialEventsAnnotationKey)

	// Ordinary events keep flowing afterwards, and so do later bookmarks.
	fakeWatcher1.Add(createResource("default", "foo"))
	require.Equal(t, watch.Added, receive().Type)
	fakeWatcher2.Action(watch.Bookmark, initialEventsEndBookmark())
	require.Equal(t, watch.Bookmark, receive().Type)
}

// TestWildcardWatchListWithNoIdentities asserts that a WatchList gets NotFound
// rather than an empty watch when the resource is served under no identity
// here: an empty watch never sends the bookmark the client waits for.
func TestWildcardWatchListWithNoIdentities(t *testing.T) {
	t.Parallel()
	fakeClient := newMultiIdentityFakeClient(t)
	storage, _, _ := newStorageWithIdentities(t, fakeClient, func(context.Context) []string { return nil }, nil)
	ctx := request.WithNamespace(context.Background(), "")
	ctx = request.WithCluster(ctx, request.Cluster{Wildcard: true})

	_, err := storage.(rest.Watcher).Watch(ctx, &internalversion.ListOptions{SendInitialEvents: ptr.To(true)})
	require.True(t, errors.IsNotFound(err), "expected NotFound for a WatchList with no identities, got %v", err)

	// Without SendInitialEvents the empty watch is still the right answer.
	watcher, err := storage.(rest.Watcher).Watch(ctx, &internalversion.ListOptions{})
	require.NoError(t, err)
	t.Cleanup(watcher.Stop)
	require.Empty(t, fakeClient.Actions(), "the shard must not be called when nothing is served here")
}
