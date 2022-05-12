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

	"github.com/stretchr/testify/require"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver"
	"k8s.io/apiextensions-apiserver/pkg/registry/customresource"
	"k8s.io/apiextensions-apiserver/pkg/registry/customresource/tableconvertor"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/generic/registry"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	kubernetestesting "k8s.io/client-go/testing"
	"k8s.io/client-go/util/retry"

	"github.com/kcp-dev/kcp/pkg/virtual/framework/forwardingregistry"
)

type mockedClientGetter struct {
	*fake.FakeDynamicClient
}

func (mcg *mockedClientGetter) GetDynamicClient(ctx context.Context) (dynamic.Interface, error) {
	return mcg.FakeDynamicClient, nil
}

func newStorage(t *testing.T, clientGetter forwardingregistry.ClientGetter, patchConflictRetryBackoff *wait.Backoff) customresource.CustomResourceStorage {
	groupResource := schema.GroupResource{Group: "mygroup.example.com", Resource: "noxus"}
	gvr := groupResource.WithVersion("v1beta1")
	groupVersion := gvr.GroupVersion()

	parameterScheme := runtime.NewScheme()
	parameterScheme.AddUnversionedTypes(groupVersion,
		&metav1.ListOptions{},
		&metav1.GetOptions{},
		&metav1.DeleteOptions{},
	)

	typer := apiserver.NewUnstructuredObjectTyper(parameterScheme)

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

	return forwardingregistry.NewStorage(
		gvr,
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
		clientGetter,
		patchConflictRetryBackoff)
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
			},
			"spec": map[string]interface{}{
				"replicas":         int64(7),
				"string":           "string",
				"float64":          float64(3.1415926),
				"bool":             true,
				"stringList":       []interface{}{"foo", "bar"},
				"mixedList":        []interface{}{"foo", int64(42)},
				"nonPrimitiveList": []interface{}{"foo", []interface{}{int64(1), int64(2)}},
			},
		},
	}
}

func TestGet(t *testing.T) {
	fakeClient := fake.NewSimpleDynamicClient(runtime.NewScheme())
	storage := newStorage(t, &mockedClientGetter{fakeClient}, nil)
	ctx := request.WithNamespace(context.Background(), "default")

	_, err := storage.CustomResource.Get(ctx, "foo", &metav1.GetOptions{})
	require.EqualError(t, err, "noxus.mygroup.example.com \"foo\" not found")

	resource := createResource("default", "foo")
	_ = fakeClient.Tracker().Add(resource)

	result, err := storage.CustomResource.Get(ctx, "foo", &metav1.GetOptions{})
	require.NoError(t, err)
	require.Truef(t, apiequality.Semantic.DeepEqual(resource, result), "expected:\n%V\nactual:\n%V", resource, result)
}

func TestList(t *testing.T) {
	resources := []runtime.Object{createResource("default", "foo"), createResource("default", "foo2")}
	fakeClient := fake.NewSimpleDynamicClient(runtime.NewScheme(), resources...)
	storage := newStorage(t, &mockedClientGetter{fakeClient}, nil)
	ctx := request.WithNamespace(context.Background(), "default")

	result, err := storage.CustomResource.List(ctx, &internalversion.ListOptions{})
	require.NoError(t, err)
	require.IsType(t, &unstructured.UnstructuredList{}, result)
	resultResources := result.(*unstructured.UnstructuredList).Items
	require.Len(t, resultResources, len(resources))
	for i, resource := range resources {
		resource := *resource.(*unstructured.Unstructured)
		require.Truef(t, apiequality.Semantic.DeepEqual(resource, resultResources[i]), "expected:\n%V\nactual:\n%V", resource, resultResources[0])
	}
}

func TestWatch(t *testing.T) {
	resources := []runtime.Object{createResource("default", "foo"), createResource("default", "foo2")}
	fakeClient := fake.NewSimpleDynamicClient(runtime.NewScheme())
	fakeWatcher := watch.NewFake()
	defer fakeWatcher.Stop()
	fakeClient.PrependWatchReactor("noxus", kubernetestesting.DefaultWatchReactor(fakeWatcher, nil))
	storage := newStorage(t, &mockedClientGetter{fakeClient}, nil)
	ctx := request.WithNamespace(context.Background(), "default")

	watchedError := &v1.Status{
		Status:  "Failure",
		Message: "message",
	}

	watchingStarted := make(chan bool, 1)
	go func() {
		<-watchingStarted
		for _, resource := range resources {
			fakeWatcher.Add(resource)
		}

		fakeWatcher.Modify(resources[0])
		fakeWatcher.Delete(resources[1])
		fakeWatcher.Error(watchedError)
	}()

	watcher, err := storage.CustomResource.Watch(ctx, &internalversion.ListOptions{})
	require.NoError(t, err)

	watchingStarted <- true
	watcherChan := watcher.ResultChan()
	var event watch.Event

	select {
	case event = <-watcherChan:
	case <-time.After(wait.ForeverTestTimeout):
		require.Fail(t, "Watch event not received")
	}
	require.Equal(t, watch.Added, event.Type, "Event type is wrong")
	require.True(t, apiequality.Semantic.DeepEqual(resources[0], event.Object), "expected:\n%V\nactual:\n%V", resources[0], event.Object)

	select {
	case event = <-watcherChan:
	case <-time.After(wait.ForeverTestTimeout):
		require.Fail(t, "Watch event not received")
	}
	require.Equal(t, watch.Added, event.Type, "Event type is wrong")
	require.True(t, apiequality.Semantic.DeepEqual(resources[1], event.Object), "expected:\n%V\nactual:\n%V", resources[1], event.Object)

	select {
	case event = <-watcherChan:
	case <-time.After(wait.ForeverTestTimeout):
		require.Fail(t, "Watch event not received")
	}
	require.Equal(t, watch.Modified, event.Type, "Event type is wrong")
	require.True(t, apiequality.Semantic.DeepEqual(resources[0], event.Object), "expected:\n%V\nactual:\n%V", resources[0], event.Object)

	select {
	case event = <-watcherChan:
	case <-time.After(wait.ForeverTestTimeout):
		require.Fail(t, "Watch event not received")
	}
	require.Equal(t, watch.Deleted, event.Type, "Event type is wrong")
	require.True(t, apiequality.Semantic.DeepEqual(resources[1], event.Object), "expected:\n%V\nactual:\n%V", resources[1], event.Object)

	select {
	case event = <-watcherChan:
	case <-time.After(wait.ForeverTestTimeout):
		require.Fail(t, "Watch event not received")
	}
	require.Equal(t, watch.Error, event.Type, "Event type is wrong")
	require.True(t, apiequality.Semantic.DeepEqual(watchedError, event.Object))
}

func updateReactor(fakeClient *fake.FakeDynamicClient) kubernetestesting.ReactionFunc {
	return func(action kubernetestesting.Action) (handled bool, ret runtime.Object, err error) {
		updateAction := action.(kubernetestesting.UpdateAction)
		actionResource := updateAction.GetObject().(*unstructured.Unstructured)

		existingObject, err := fakeClient.Tracker().Get(action.GetResource(), action.GetNamespace(), actionResource.GetName())
		if err != nil {
			return true, nil, err
		}
		existingResource := existingObject.(*unstructured.Unstructured)

		if existingResource.GetResourceVersion() != actionResource.GetResourceVersion() {
			return true, nil, errors.NewConflict(action.GetResource().GroupResource(), existingResource.GetName(), fmt.Errorf(registry.OptimisticLockErrorMsg))
		}
		if err := fakeClient.Tracker().Update(action.GetResource(), actionResource, action.GetNamespace()); err != nil {
			return true, nil, err
		}

		return true, actionResource, nil
	}
}
func TestUpdate(t *testing.T) {
	resource := createResource("default", "foo")
	resource.SetGeneration(1)
	resource.SetResourceVersion("100")
	fakeClient := fake.NewSimpleDynamicClient(runtime.NewScheme())
	fakeClient.PrependReactor("update", "noxus", updateReactor(fakeClient))

	storage := newStorage(t, &mockedClientGetter{fakeClient}, nil)
	ctx := request.WithNamespace(context.Background(), "default")
	updated := resource.DeepCopy()

	newReplicas, _, err := unstructured.NestedInt64(updated.UnstructuredContent(), "spec", "replicas")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	newReplicas++
	_ = unstructured.SetNestedField(updated.UnstructuredContent(), newReplicas, "spec", "replicas")

	updatedWithStatusChanged := updated.DeepCopy()
	if err := unstructured.SetNestedField(updatedWithStatusChanged.UnstructuredContent(), int64(10), "status", "availableReplicas"); err != nil {
		require.NoError(t, err)
	}

	_, _, err = storage.CustomResource.Update(ctx, updated.GetName(), rest.DefaultUpdatedObjectInfo(updatedWithStatusChanged), rest.ValidateAllObjectFunc, rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{})
	require.EqualError(t, err, "noxus.mygroup.example.com \"foo\" not found")

	_ = fakeClient.Tracker().Add(resource)
	result, _, err := storage.CustomResource.Update(ctx, updated.GetName(), rest.DefaultUpdatedObjectInfo(updatedWithStatusChanged), rest.ValidateAllObjectFunc, rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{})
	resultResource := result.(*unstructured.Unstructured)
	require.NoError(t, err)
	updatedGeneration, _, err := unstructured.NestedInt64(resultResource.UnstructuredContent(), "metadata", "generation")
	require.NoError(t, err)
	require.Equalf(t, int64(2), updatedGeneration, "Generation should be incremented when updating the Spec")

	// We just checked that the generation has been increased. Now reset the generation to the initial value:
	// this will allow testing the deep equality of the objects, apart from the generation number.
	_ = unstructured.SetNestedField(resultResource.UnstructuredContent(), int64(1), "metadata", "generation")

	// Now we can check that the status has in fact not been updated
	require.True(t, apiequality.Semantic.DeepEqual(updated, result), "expected:\n%V\nactual:\n%V", updated, result)

	fakeClient.ClearActions()
	updated.SetResourceVersion("101")
	newReplicas++
	_ = unstructured.SetNestedField(updated.UnstructuredContent(), newReplicas, "spec", "replicas")
	_, _, err = storage.CustomResource.Update(ctx, updated.GetName(), rest.DefaultUpdatedObjectInfo(updated), rest.ValidateAllObjectFunc, rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{})
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
	fakeClient := fake.NewSimpleDynamicClient(runtime.NewScheme(), resource)
	fakeClient.PrependReactor("update", "noxus", updateReactor(fakeClient))

	storage := newStorage(t, &mockedClientGetter{fakeClient}, nil)
	ctx := request.WithNamespace(context.Background(), "default")
	statusUpdated := resource.DeepCopy()
	if err := unstructured.SetNestedField(statusUpdated.UnstructuredContent(), int64(10), "status", "availableReplicas"); err != nil {
		require.NoError(t, err)
	}

	statusUpdatedWithSpecChanged := statusUpdated.DeepCopy()
	newReplicas, _, err := unstructured.NestedInt64(statusUpdated.UnstructuredContent(), "spec", "replicas")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	newReplicas++
	_ = unstructured.SetNestedField(statusUpdatedWithSpecChanged.UnstructuredContent(), newReplicas, "spec", "replicas")

	result, _, err := storage.Status.Update(ctx, statusUpdated.GetName(), rest.DefaultUpdatedObjectInfo(statusUpdatedWithSpecChanged), rest.ValidateAllObjectFunc, rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{})
	resultResource := result.(*unstructured.Unstructured)
	require.NoError(t, err)
	updatedGeneration, _, err := unstructured.NestedInt64(resultResource.UnstructuredContent(), "metadata", "generation")
	require.NoError(t, err)
	require.Equalf(t, int64(1), updatedGeneration, "Generation should not be incremented when updating the Status")

	// We check that the spec has in fact not been updated
	require.True(t, apiequality.Semantic.DeepEqual(statusUpdated, result), "expected:\n%V\nactual:\n%V", statusUpdated, result)
}

func TestPatch(t *testing.T) {
	resource := createResource("default", "foo")
	resource.SetGeneration(1)
	resource.SetResourceVersion("100")
	fakeClient := fake.NewSimpleDynamicClient(runtime.NewScheme())
	fakeClient.PrependReactor("update", "noxus", updateReactor(fakeClient))

	backoff := retry.DefaultRetry
	backoff.Steps = 5
	storage := newStorage(t, &mockedClientGetter{fakeClient}, &backoff)
	ctx := request.WithNamespace(context.Background(), "default")
	ctx = request.WithRequestInfo(ctx, &request.RequestInfo{Verb: "patch"})

	patcher := func(ctx context.Context, newObj, oldObj runtime.Object) (runtime.Object, error) {
		updated := oldObj.DeepCopyObject().(*unstructured.Unstructured)
		newReplicas, _, err := unstructured.NestedInt64(updated.UnstructuredContent(), "spec", "replicas")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		newReplicas++
		_ = unstructured.SetNestedField(updated.UnstructuredContent(), newReplicas, "spec", "replicas")
		return updated, nil
	}

	_, _, err := storage.CustomResource.Update(ctx, resource.GetName(), rest.DefaultUpdatedObjectInfo(nil, patcher), rest.ValidateAllObjectFunc, rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{})
	require.EqualError(t, err, "noxus.mygroup.example.com \"foo\" not found")

	_ = fakeClient.Tracker().Add(resource)
	getCallCounts := 0
	noMoreConflicts := 4
	fakeClient.PrependReactor("get", "noxus", func(action kubernetestesting.Action) (handled bool, ret runtime.Object, err error) {
		getCallCounts++
		if getCallCounts < noMoreConflicts {
			withChangedResourceVersion := resource.DeepCopy()
			withChangedResourceVersion.SetResourceVersion("50")
			return true, withChangedResourceVersion, nil
		}
		return true, resource, nil
	})

	resultObj, _, err := storage.CustomResource.Update(ctx, resource.GetName(), rest.DefaultUpdatedObjectInfo(nil, patcher), rest.ValidateAllObjectFunc, rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{})
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

	resultGeneration, _, err := unstructured.NestedInt64(result.UnstructuredContent(), "metadata", "generation")
	require.NoError(t, err)
	require.Equalf(t, int64(2), resultGeneration, "Generation should be incremented when patching the Spec")

	// We just checked that the generation has been increased. Now reset the generation to the initial value:
	// this will allow testing the deep equality of the objects, apart from the generation number.
	result.SetGeneration(1)
	require.True(t, apiequality.Semantic.DeepEqual(expected, result), "expected:\n%V\nactual:\n%V", expected, result)

	getCallCounts = 0
	noMoreConflicts = backoff.Steps + 1
	fakeClient.ClearActions()

	_, _, err = storage.CustomResource.Update(ctx, resource.GetName(), rest.DefaultUpdatedObjectInfo(nil, patcher), rest.ValidateAllObjectFunc, rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{})
	require.EqualError(t, err, "Operation cannot be fulfilled on noxus.mygroup.example.com \"foo\": the object has been modified; please apply your changes to the latest version and try again")

	updates = 0
	for _, action := range fakeClient.Actions() {
		if action.GetVerb() == "update" {
			updates++
		}
	}
	require.Equalf(t, backoff.Steps, updates, "Should have tried calling client.Update %d times to overcome resourceVersion conflicts, before finally returning a Conflict error.", backoff.Steps)
}
