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

package forwardingregistry_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	kcpfakedynamic "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/dynamic/fake"
	kcptesting "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver"
	"k8s.io/apiextensions-apiserver/pkg/crdserverscheme"
	"k8s.io/apiextensions-apiserver/pkg/registry/customresource"
	"k8s.io/apiextensions-apiserver/pkg/registry/customresource/tableconvertor"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
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

	"github.com/kcp-dev/kcp/pkg/virtual/framework/forwardingregistry"
)

var noxusGVR = schema.GroupVersionResource{Group: "mygroup.example.com", Resource: "noxus", Version: "v1beta1"}

func newStorage(t *testing.T, clusterClient kcpdynamic.ClusterInterface, apiExportIdentityHash string, patchConflictRetryBackoff *wait.Backoff) (mainStorage, statusStorage rest.Storage) {
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

	return forwardingregistry.NewStorage(
		ctx,
		gvr,
		apiExportIdentityHash,
		kind,
		listKind,
		customresource.NewStrategy(
			typer,
			true,
			kind,
			nil,
			nil,
			nil,
			&apiextensions.CustomResourceSubresourceStatus{},
			nil),
		nil,
		table,
		nil,
		clusterClient,
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
	fakeClient := kcpfakedynamic.NewSimpleDynamicClient(runtime.NewScheme())
	storage, _ := newStorage(t, fakeClient, "", nil)
	ctx := request.WithNamespace(context.Background(), "default")
	ctx = request.WithCluster(ctx, request.Cluster{Name: "test"})

	getter := storage.(rest.Getter)
	_, err := getter.Get(ctx, "foo", &metav1.GetOptions{})
	require.EqualError(t, err, "noxus.mygroup.example.com \"foo\" not found")

	resource := createResource("default", "foo")
	_ = fakeClient.Tracker().Cluster(logicalcluster.New("test")).Add(resource)

	result, err := getter.Get(ctx, "foo", &metav1.GetOptions{})
	require.NoError(t, err)
	require.Truef(t, apiequality.Semantic.DeepEqual(resource, result), "expected:\n%V\nactual:\n%V", resource, result)
}

func TestList(t *testing.T) {
	resources := []runtime.Object{createResource("default", "foo"), createResource("default", "foo2")}
	fakeClient := kcpfakedynamic.NewSimpleDynamicClient(runtime.NewScheme(), resources...)
	storage, _ := newStorage(t, fakeClient, "", nil)
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
		require.Truef(t, apiequality.Semantic.DeepEqual(resource, resultResources[i]), "expected:\n%V\nactual:\n%V", resource, resultResources[0])
	}
	require.Len(t, fakeClient.Actions(), 1)
	require.Equal(t, "noxus", fakeClient.Actions()[0].GetResource().Resource)
}

func TestWildcardListWithAPIExportIdentity(t *testing.T) {
	resources := []runtime.Object{createResource("default", "foo"), createResource("default", "foo2")}
	noxusGVRWithHash := noxusGVR.GroupVersion().WithResource("noxus:" + "apiExportIdentityHash")
	fakeClient := kcpfakedynamic.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			noxusGVR:         "NoxuList",
			noxusGVRWithHash: "NoxuList",
		})
	for _, resource := range resources {
		_ = fakeClient.Tracker().Cluster(logicalcluster.New("test")).Create(noxusGVRWithHash, resource, "default")
	}

	storage, _ := newStorage(t, fakeClient, "apiExportIdentityHash", nil)
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
		require.Truef(t, apiequality.Semantic.DeepEqual(resource, resultResources[i]), "expected:\n%V\nactual:\n%V", resource, resultResources[0])
	}
	require.Len(t, fakeClient.Actions(), 1)
	require.Equal(t, "noxus:apiExportIdentityHash", fakeClient.Actions()[0].GetResource().Resource)
}

func checkWatchEvents(t *testing.T, addEvents func(), watchCall func() (watch.Interface, error), expectedEvents []watch.Event) {
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
	resources := []runtime.Object{createResource("default", "foo"), createResource("default", "foo2")}
	fakeClient := kcpfakedynamic.NewSimpleDynamicClient(runtime.NewScheme())
	fakeWatcher := watch.NewFake()
	defer fakeWatcher.Stop()
	fakeClient.PrependWatchReactor("noxus", kcptesting.DefaultWatchReactor(fakeWatcher, nil))
	storage, _ := newStorage(t, fakeClient, "", nil)
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
	resources := []runtime.Object{createResource("default", "foo"), createResource("default", "foo2")}
	noxusGVRWithHash := noxusGVR.GroupVersion().WithResource("noxus:apiExportIdentityHash")
	fakeClient := kcpfakedynamic.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			noxusGVR:         "NoxuList",
			noxusGVRWithHash: "NoxuList",
		})
	fakeWatcher := watch.NewFake()
	defer fakeWatcher.Stop()
	fakeClient.PrependWatchReactor("noxus:apiExportIdentityHash", kcptesting.DefaultWatchReactor(fakeWatcher, nil))
	storage, _ := newStorage(t, fakeClient, "apiExportIdentityHash", nil)
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

func updateReactor(fakeClient *kcpfakedynamic.FakeDynamicClusterClientset) kcptesting.ReactionFunc {
	return func(action kcptesting.Action) (handled bool, ret runtime.Object, err error) {
		updateAction := action.(kcptesting.UpdateAction)
		actionResource := updateAction.GetObject().(*unstructured.Unstructured)

		existingObject, err := fakeClient.Tracker().Cluster(logicalcluster.New("test")).Get(action.GetResource(), action.GetNamespace(), actionResource.GetName())
		if err != nil {
			return true, nil, err
		}
		existingResource := existingObject.(*unstructured.Unstructured)

		if existingResource.GetResourceVersion() != actionResource.GetResourceVersion() {
			return true, nil, errors.NewConflict(action.GetResource().GroupResource(), existingResource.GetName(), fmt.Errorf(registry.OptimisticLockErrorMsg))
		}
		if err := fakeClient.Tracker().Cluster(logicalcluster.New("test")).Update(action.GetResource(), actionResource, action.GetNamespace()); err != nil {
			return true, nil, err
		}

		return true, actionResource, nil
	}
}

func TestUpdate(t *testing.T) {
	resource := createResource("default", "foo")
	resource.SetGeneration(1)
	resource.SetResourceVersion("100")
	fakeClient := kcpfakedynamic.NewSimpleDynamicClient(runtime.NewScheme())
	fakeClient.PrependReactor("update", "noxus", updateReactor(fakeClient))

	storage, _ := newStorage(t, fakeClient, "", nil)
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

	_ = fakeClient.Tracker().Cluster(logicalcluster.New("test")).Add(resource)
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
	resource := createResource("default", "foo")
	resource.SetGeneration(1)
	resource.SetResourceVersion("100")
	fakeClient := kcpfakedynamic.NewSimpleDynamicClient(runtime.NewScheme())
	fakeClient.PrependReactor("update", "noxus", updateReactor(fakeClient))

	storage, _ := newStorage(t, fakeClient, "", nil)
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
	resource := createResource("default", "foo")
	resource.SetGeneration(1)
	resource.SetResourceVersion("100")
	fakeClient := kcpfakedynamic.NewSimpleDynamicClient(runtime.NewScheme(), resource)
	fakeClient.PrependReactor("update", "noxus", updateReactor(fakeClient))

	_, statusStorage := newStorage(t, fakeClient, "", nil)
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
	resource := createResource("default", "foo")
	resource.SetGeneration(1)
	resource.SetResourceVersion("100")
	fakeClient := kcpfakedynamic.NewSimpleDynamicClient(runtime.NewScheme())
	fakeClient.PrependReactor("update", "noxus", updateReactor(fakeClient))

	backoff := retry.DefaultRetry
	backoff.Steps = 5
	storage, _ := newStorage(t, fakeClient, "", &backoff)
	ctx := request.WithNamespace(context.Background(), "default")
	ctx = request.WithRequestInfo(ctx, &request.RequestInfo{Verb: "patch"})
	ctx = request.WithCluster(ctx, request.Cluster{Name: "test"})

	patcher := func(ctx context.Context, newObj, oldObj runtime.Object) (runtime.Object, error) {
		if oldObj == nil {
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

	_ = fakeClient.Tracker().Cluster(logicalcluster.New("test")).Add(resource)
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
