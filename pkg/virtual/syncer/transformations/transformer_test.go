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

package transformations

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/kcp-dev/kcp/pkg/apis/workload/helpers"
	"github.com/kcp-dev/kcp/pkg/client"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/transforming"
	syncercontext "github.com/kcp-dev/kcp/pkg/virtual/syncer/context"
)

type mockedClusterClient struct {
	client           *fake.FakeDynamicClient
	lclusterRecorder func(lcluster string)
}

func (c *mockedClusterClient) Resource(resource schema.GroupVersionResource) kcpdynamic.ResourceClusterInterface {
	return &mockedResourceClusterClient{
		resourceClient: resourceClient{
			client:            c.client,
			resource:          resource,
			resourceInterface: c.client.Resource(resource),
			lclusterRecorder:  c.lclusterRecorder,
		},
	}
}

func (c *mockedClusterClient) Cluster(cluster logicalcluster.Path) dynamic.Interface {
	return &dynamicClient{
		client:           c.client,
		lcluster:         cluster,
		lclusterRecorder: c.lclusterRecorder,
	}
}

type mockedResourceClusterClient struct {
	resourceClient
}

func (c *mockedResourceClusterClient) Cluster(lcluster logicalcluster.Path) dynamic.NamespaceableResourceInterface {
	return &namespaceableResourceClient{
		resourceClient: resourceClient{
			resourceInterface: c.client.Resource(c.resource),
			client:            c.client,
			resource:          c.resource,
			lcluster:          lcluster,
			lclusterRecorder:  c.lclusterRecorder,
		},
	}
}

func (c *mockedResourceClusterClient) List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	return c.resourceClient.List(ctx, opts)
}

func (c *mockedResourceClusterClient) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	return c.resourceClient.Watch(ctx, opts)
}

type dynamicClient struct {
	client           *fake.FakeDynamicClient
	lcluster         logicalcluster.Path
	lclusterRecorder func(lcluster string)
}

func (c *dynamicClient) ClusterName() string { return c.lcluster.String() }

func (c *dynamicClient) Resource(resource schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return &namespaceableResourceClient{
		resourceClient: resourceClient{
			resourceInterface: c.client.Resource(resource),
			client:            c.client,
			lcluster:          c.lcluster,
			resource:          resource,
			lclusterRecorder:  c.lclusterRecorder,
		},
	}
}

type namespaceableResourceClient struct {
	resourceClient
}

func (c *namespaceableResourceClient) ClusterName() string { return c.lcluster.String() }

func (c *namespaceableResourceClient) Namespace(namespace string) dynamic.ResourceInterface {
	return &resourceClient{
		resourceInterface: c.client.Resource(c.resource).Namespace(namespace),
		client:            c.client,
		lcluster:          c.lcluster,
		resource:          c.resource,
		namespace:         namespace,
		lclusterRecorder:  c.lclusterRecorder,
	}
}

type resourceClient struct {
	resourceInterface dynamic.ResourceInterface
	client            *fake.FakeDynamicClient
	lcluster          logicalcluster.Path
	resource          schema.GroupVersionResource
	namespace         string
	lclusterRecorder  func(lcluster string)
}

func (c *resourceClient) ClusterName() string { return c.lcluster.String() }

func (c *resourceClient) RawDelete(ctx context.Context, name string, opts metav1.DeleteOptions, subresources ...string) ([]byte, int, error) {
	obj, _ := c.client.Tracker().Get(c.resource, c.namespace, name)
	err := c.Delete(ctx, name, opts, subresources...)
	bytes, _ := runtime.Encode(unstructured.UnstructuredJSONScheme, obj)
	return bytes, 0, err
}

func (c *resourceClient) RawDeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOptions metav1.ListOptions) ([]byte, int, error) {
	gvk := c.resource.GroupVersion().WithKind(strings.TrimRight(strings.Title(c.resource.Resource), "s")) //nolint:staticcheck
	objs, _ := c.client.Tracker().List(c.resource, gvk, c.namespace)
	list := objs.(*unstructured.UnstructuredList)
	list.SetGroupVersionKind(schema.GroupVersionKind{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind + "List"})
	list.SetResourceVersion("")
	err := c.DeleteCollection(ctx, opts, listOptions)
	bytes, _ := runtime.Encode(unstructured.UnstructuredJSONScheme, list)
	return bytes, 0, err
}

func (c *resourceClient) Create(ctx context.Context, obj *unstructured.Unstructured, options metav1.CreateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	c.lclusterRecorder(c.lcluster.String())
	return c.resourceInterface.Create(ctx, obj, options, subresources...)
}
func (c *resourceClient) Update(ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	c.lclusterRecorder(c.lcluster.String())
	return c.resourceInterface.Update(ctx, obj, options, subresources...)
}
func (c *resourceClient) UpdateStatus(ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	c.lclusterRecorder(c.lcluster.String())
	return c.resourceInterface.UpdateStatus(ctx, obj, options)
}
func (c *resourceClient) Delete(ctx context.Context, name string, options metav1.DeleteOptions, subresources ...string) error {
	c.lclusterRecorder(c.lcluster.String())
	return c.resourceInterface.Delete(ctx, name, options, subresources...)
}
func (c *resourceClient) DeleteCollection(ctx context.Context, options metav1.DeleteOptions, listOptions metav1.ListOptions) error {
	c.lclusterRecorder(c.lcluster.String())
	return c.resourceInterface.DeleteCollection(ctx, options, listOptions)
}
func (c *resourceClient) Get(ctx context.Context, name string, options metav1.GetOptions, subresources ...string) (*unstructured.Unstructured, error) {
	c.lclusterRecorder(c.lcluster.String())
	return c.resourceInterface.Get(ctx, name, options, subresources...)
}
func (c *resourceClient) List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	c.lclusterRecorder(c.lcluster.String())
	return c.resourceInterface.List(ctx, opts)
}
func (c *resourceClient) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	c.lclusterRecorder(c.lcluster.String())
	return c.resourceInterface.Watch(ctx, opts)
}
func (c *resourceClient) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, options metav1.PatchOptions, subresources ...string) (*unstructured.Unstructured, error) {
	c.lclusterRecorder(c.lcluster.String())
	return c.resourceInterface.Patch(ctx, name, pt, data, options, subresources...)
}

func gvr(group, version, resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    group,
		Version:  version,
		Resource: resource,
	}
}

type resourceBuilder func() *unstructured.Unstructured

func (rb resourceBuilder) annotation(key, value string) resourceBuilder {
	return func() *unstructured.Unstructured {
		r := rb.annotations()()
		r.Object["metadata"].(map[string]interface{})["annotations"].(map[string]interface{})[key] = value
		return r
	}
}

func (rb resourceBuilder) annotations() resourceBuilder {
	return func() *unstructured.Unstructured {
		r := rb()
		metadata := r.Object["metadata"].(map[string]interface{})
		if _, exists := metadata["annotations"]; !exists {
			metadata["annotations"] = map[string]interface{}{}

		}
		return r
	}
}

func (rb resourceBuilder) resourceVersion(rv string) resourceBuilder {
	return func() *unstructured.Unstructured {
		r := rb()
		r.SetResourceVersion(rv)
		return r
	}
}

func (rb resourceBuilder) deletionTimestamp(timestamp *metav1.Time) resourceBuilder {
	return func() *unstructured.Unstructured {
		r := rb()
		r.SetDeletionTimestamp(timestamp)
		return r
	}
}

func (rb resourceBuilder) label(key, value string) resourceBuilder {
	return func() *unstructured.Unstructured {
		r := rb.labels()()
		r.Object["metadata"].(map[string]interface{})["labels"].(map[string]interface{})[key] = value
		return r
	}
}

func (rb resourceBuilder) labels() resourceBuilder {
	return func() *unstructured.Unstructured {
		r := rb()
		metadata := r.Object["metadata"].(map[string]interface{})
		if _, exists := metadata["labels"]; !exists {
			metadata["labels"] = map[string]interface{}{}

		}
		return r
	}
}

func (rb resourceBuilder) finalizer(finalizer string) resourceBuilder {
	return func() *unstructured.Unstructured {
		r := rb()
		finalizers := r.GetFinalizers()
		finalizers = append(finalizers, finalizer)
		r.SetFinalizers(finalizers)
		return r
	}
}

func (rb resourceBuilder) finalizers() resourceBuilder {
	return func() *unstructured.Unstructured {
		r := rb()
		finalizers := r.GetFinalizers()
		if finalizers == nil {
			r.SetFinalizers([]string{})
		}
		return r
	}
}

func (rb resourceBuilder) field(name string, value interface{}) resourceBuilder {
	return func() *unstructured.Unstructured {
		r := rb()
		r.Object[name] = value
		return r
	}
}

func resource(apiVersion, kind, name string) resourceBuilder {
	return func() *unstructured.Unstructured {
		return &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": apiVersion,
				"kind":       kind,
				"metadata": map[string]interface{}{
					"name": name,
				},
			},
		}
	}
}

type mockedTransformation struct {
	transform func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error)
}

func (mt *mockedTransformation) ToSyncerView(SyncTargetKey string, gvr schema.GroupVersionResource, upstreamResource *unstructured.Unstructured, overridenSyncerViewFields map[string]interface{}, requestedSyncing map[string]helpers.SyncIntent) (newSyncerViewResource *unstructured.Unstructured, err error) {
	return mt.transform(upstreamResource)
}

func (mt *mockedTransformation) TransformationFor(resource metav1.Object) (Transformation, error) {
	return mt, nil
}

type mockedSummarizingRules struct {
	fields []field
}

func (msr *mockedSummarizingRules) FieldsToSummarize(gvr schema.GroupVersionResource) []FieldToSummarize {
	var result []FieldToSummarize
	for _, f := range msr.fields {
		result = append(result, FieldToSummarize(f))
	}
	return result
}

func (msr *mockedSummarizingRules) SummarizingRulesFor(resource metav1.Object) (SummarizingRules, error) {
	return msr, nil
}

var deletionTimestamp = metav1.Now()

func TestSyncerResourceTransformer(t *testing.T) {
	testCases := []struct {
		name                  string
		gvr                   schema.GroupVersionResource
		availableResources    []runtime.Object
		action                func(ctx context.Context, transformingClient dynamic.NamespaceableResourceInterface) (result interface{}, err error)
		synctargetKey         string
		transform             func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error)
		fieldsToSummarize     []field
		expectedResult        interface{}
		checkResult           func(t *testing.T, watchTester *watch.FakeWatcher, result interface{})
		expectedError         string
		expectedClientActions []clienttesting.Action
	}{
		{
			name:          "get - transform - summarize - success",
			gvr:           gvr("group", "version", "resources"),
			synctargetKey: "syncTargetKey",
			availableResources: []runtime.Object{
				resource("group/version", "Resource", "aThing").
					annotation("diff.syncer.internal.kcp.dev/syncTargetKey", `{"status":{"statusField":"new"}}`)(),
			},
			action: func(ctx context.Context, transformingClient dynamic.NamespaceableResourceInterface) (result interface{}, err error) {
				return transformingClient.Get(ctx, "aThing", metav1.GetOptions{})
			},
			transform: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "value", "added")
				return result, nil
			},
			fieldsToSummarize: []field{{FieldPath: "status"}},
			expectedClientActions: []clienttesting.Action{
				clienttesting.GetActionImpl{
					ActionImpl: clienttesting.ActionImpl{
						Verb:     "get",
						Resource: gvr("group", "version", "resources"),
					},
					Name: "aThing",
				},
			},
			expectedResult: resource("group/version", "Resource", "aThing").annotations().
				field("added", "value").
				field("status", map[string]interface{}{"statusField": "new"})(),
		},
		{
			name:          "get - deletion requested - success",
			gvr:           gvr("group", "version", "resources"),
			synctargetKey: "syncTargetKey",
			availableResources: []runtime.Object{
				resource("group/version", "Resource", "aThing").
					label("state.workload.kcp.dev/syncTargetKey", "Sync").
					annotation("deletion.internal.workload.kcp.dev/syncTargetKey", deletionTimestamp.Format(time.RFC3339))(),
			},
			action: func(ctx context.Context, transformingClient dynamic.NamespaceableResourceInterface) (result interface{}, err error) {
				return transformingClient.Get(ctx, "aThing", metav1.GetOptions{})
			},
			fieldsToSummarize: []field{{FieldPath: "status"}},
			expectedClientActions: []clienttesting.Action{
				clienttesting.GetActionImpl{
					ActionImpl: clienttesting.ActionImpl{
						Verb:     "get",
						Resource: gvr("group", "version", "resources"),
					},
					Name: "aThing",
				},
			},
			expectedResult: resource("group/version", "Resource", "aThing").
				label("state.workload.kcp.dev/syncTargetKey", "Sync").
				annotation("deletion.internal.workload.kcp.dev/syncTargetKey", deletionTimestamp.Format(time.RFC3339)).
				deletionTimestamp(&deletionTimestamp)(),
		},
		{
			name:          "update status - no promote - success",
			gvr:           gvr("group", "version", "resources"),
			synctargetKey: "syncTargetKey",
			availableResources: []runtime.Object{
				resource("group/version", "Resource", "aThing").
					label("state.workload.kcp.dev/syncTargetKey", "Sync").
					annotation("diff.syncer.internal.kcp.dev/syncTargetKey", `{"spec.field":"alreadyupdated"}`)(),
			},
			action: func(ctx context.Context, transformingClient dynamic.NamespaceableResourceInterface) (result interface{}, err error) {
				return transformingClient.UpdateStatus(ctx, resource("group/version", "Resource", "aThing").
					finalizer("workload.kcp.dev/syncer-syncTargetKey").
					field("status", map[string]interface{}{"statusField": "updated"})(), metav1.UpdateOptions{})
			},
			transform: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "value", "added")
				return result, nil
			},
			fieldsToSummarize: []field{{FieldPath: "status"}, {FieldPath: "spec.field"}},
			expectedClientActions: []clienttesting.Action{
				clienttesting.GetActionImpl{
					ActionImpl: clienttesting.ActionImpl{
						Verb:     "get",
						Resource: gvr("group", "version", "resources"),
					},
					Name: "aThing",
				},
				clienttesting.UpdateActionImpl{
					ActionImpl: clienttesting.ActionImpl{
						Verb:        "update",
						Resource:    gvr("group", "version", "resources"),
						Subresource: "status",
					},
					Object: resource("group/version", "Resource", "aThing").
						finalizer("workload.kcp.dev/syncer-syncTargetKey").
						label("state.workload.kcp.dev/syncTargetKey", "Sync").
						annotation("diff.syncer.internal.kcp.dev/syncTargetKey", `{"spec.field":"alreadyupdated","status":{"statusField":"updated"}}`)(),
				},
			},
			expectedResult: resource("group/version", "Resource", "aThing").
				finalizer("workload.kcp.dev/syncer-syncTargetKey").
				label("state.workload.kcp.dev/syncTargetKey", "Sync").
				annotations().
				field("status", map[string]interface{}{"statusField": "updated"}).
				field("added", "value").
				field("spec", map[string]interface{}{"field": "alreadyupdated"})(),
		},
		{
			name:          "update status - fail not owning",
			gvr:           gvr("group", "version", "resources"),
			synctargetKey: "syncTargetKey",
			availableResources: []runtime.Object{
				resource("group/version", "Resource", "aThing").
					label("state.workload.kcp.dev/syncTargetKey", "Sync").
					annotation("diff.syncer.internal.kcp.dev/syncTargetKey", `{"spec.field":"alreadyupdated"}`)(),
			},
			action: func(ctx context.Context, transformingClient dynamic.NamespaceableResourceInterface) (result interface{}, err error) {
				return transformingClient.UpdateStatus(ctx, resource("group/version", "Resource", "aThing").
					field("status", map[string]interface{}{"statusField": "updated"})(), metav1.UpdateOptions{})
			},
			transform: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "value", "added")
				return result, nil
			},
			fieldsToSummarize: []field{{FieldPath: "status"}, {FieldPath: "spec.field"}},
			expectedClientActions: []clienttesting.Action{
				clienttesting.GetActionImpl{
					ActionImpl: clienttesting.ActionImpl{
						Verb:     "get",
						Resource: gvr("group", "version", "resources"),
					},
					Name: "aThing",
				},
			},
			expectedError: `Internal error occurred: tried to write resource resources/status(/aThing) though it is not owning it (syncer finalizer doesn't exist)`,
		},
		{
			name:          "update status - promote - success",
			gvr:           gvr("group", "version", "resources"),
			synctargetKey: "syncTargetKey",
			availableResources: []runtime.Object{
				resource("group/version", "Resource", "aThing").
					label("state.workload.kcp.dev/syncTargetKey", "Sync")(),
			},
			action: func(ctx context.Context, transformingClient dynamic.NamespaceableResourceInterface) (result interface{}, err error) {
				return transformingClient.UpdateStatus(ctx, resource("group/version", "Resource", "aThing").
					finalizer("workload.kcp.dev/syncer-syncTargetKey").
					field("status", map[string]interface{}{"statusField": "updated"})(), metav1.UpdateOptions{})
			},
			transform: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "value", "added")
				return result, nil
			},
			fieldsToSummarize: []field{{FieldPath: "status", PromoteToUpstream: true}},
			expectedClientActions: []clienttesting.Action{
				clienttesting.GetActionImpl{
					ActionImpl: clienttesting.ActionImpl{
						Verb:     "get",
						Resource: gvr("group", "version", "resources"),
					},
					Name: "aThing",
				},
				clienttesting.UpdateActionImpl{
					ActionImpl: clienttesting.ActionImpl{
						Verb:        "update",
						Resource:    gvr("group", "version", "resources"),
						Subresource: "status",
					},
					Object: resource("group/version", "Resource", "aThing").
						finalizer("workload.kcp.dev/syncer-syncTargetKey").
						label("state.workload.kcp.dev/syncTargetKey", "Sync").
						annotation("diff.syncer.internal.kcp.dev/syncTargetKey", `{"status":"##promoted##"}`).
						field("status", map[string]interface{}{"statusField": "updated"})(),
				},
			},
			expectedResult: resource("group/version", "Resource", "aThing").
				finalizer("workload.kcp.dev/syncer-syncTargetKey").
				label("state.workload.kcp.dev/syncTargetKey", "Sync").
				annotations().
				field("status", map[string]interface{}{"statusField": "updated"}).
				field("added", "value")(),
		},
		{
			name:          "update status - unpromote when new synctarget joining - success",
			gvr:           gvr("group", "version", "resources"),
			synctargetKey: "syncTargetKey",
			availableResources: []runtime.Object{
				resource("group/version", "Resource", "aThing").
					finalizer("workload.kcp.dev/syncer-syncTargetKey").
					label("state.workload.kcp.dev/syncTargetKey", "Sync").
					label("state.workload.kcp.dev/syncTargetKey2", "").
					annotation("diff.syncer.internal.kcp.dev/syncTargetKey", `{"status":"##promoted##"}`).
					field("status", map[string]interface{}{"statusField": "updated"})(),
			},
			action: func(ctx context.Context, transformingClient dynamic.NamespaceableResourceInterface) (result interface{}, err error) {
				return transformingClient.UpdateStatus(ctx, resource("group/version", "Resource", "aThing").
					finalizer("workload.kcp.dev/syncer-syncTargetKey").
					field("status", map[string]interface{}{"statusField": "updated"})(), metav1.UpdateOptions{})
			},
			transform: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "value", "added")
				return result, nil
			},
			fieldsToSummarize: []field{{FieldPath: "status", PromoteToUpstream: true}},
			expectedClientActions: []clienttesting.Action{
				clienttesting.GetActionImpl{
					ActionImpl: clienttesting.ActionImpl{
						Verb:     "get",
						Resource: gvr("group", "version", "resources"),
					},
					Name: "aThing",
				},
				clienttesting.UpdateActionImpl{
					ActionImpl: clienttesting.ActionImpl{
						Verb:        "update",
						Resource:    gvr("group", "version", "resources"),
						Subresource: "status",
					},
					Object: resource("group/version", "Resource", "aThing").
						finalizer("workload.kcp.dev/syncer-syncTargetKey").
						label("state.workload.kcp.dev/syncTargetKey", "Sync").
						label("state.workload.kcp.dev/syncTargetKey2", "").
						annotation("diff.syncer.internal.kcp.dev/syncTargetKey", `{"status":{"statusField":"updated"}}`).
						field("status", map[string]interface{}{"statusField": "updated"})(),
				},
			},
			expectedResult: resource("group/version", "Resource", "aThing").
				finalizer("workload.kcp.dev/syncer-syncTargetKey").
				label("state.workload.kcp.dev/syncTargetKey", "Sync").
				label("state.workload.kcp.dev/syncTargetKey2", "").
				annotations().
				field("status", map[string]interface{}{"statusField": "updated"}).
				field("added", "value")(),
		},
		{
			name: "update - not found",
			gvr:  schema.GroupVersionResource{Group: "group", Version: "version", Resource: "resources"},
			action: func(ctx context.Context, transformingClient dynamic.NamespaceableResourceInterface) (result interface{}, err error) {
				return transformingClient.Update(ctx, resource("group/version", "Resource", "aThing").
					label("state.workload.kcp.dev/syncTargetKey", "Sync")(), metav1.UpdateOptions{})
			},
			transform: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added", "before")
				return result, nil
			},
			expectedClientActions: []clienttesting.Action{
				clienttesting.GetActionImpl{
					ActionImpl: clienttesting.ActionImpl{
						Verb:     "get",
						Resource: gvr("group", "version", "resources"),
					},
					Name: "aThing",
				},
			},
			expectedError: `resources.group "aThing" not found`,
		},
		{
			name: "update - conflict",
			gvr:  schema.GroupVersionResource{Group: "group", Version: "version", Resource: "resources"},
			availableResources: []runtime.Object{
				resource("group/version", "Resource", "aThing").
					resourceVersion("0001").
					label("state.workload.kcp.dev/syncTargetKey", "Sync")(),
			},
			action: func(ctx context.Context, transformingClient dynamic.NamespaceableResourceInterface) (result interface{}, err error) {
				return transformingClient.Update(ctx, resource("group/version", "Resource", "aThing").
					resourceVersion("0002").
					label("state.workload.kcp.dev/syncTargetKey", "Sync")(), metav1.UpdateOptions{})
			},
			transform: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added", "before")
				return result, nil
			},
			expectedClientActions: []clienttesting.Action{
				clienttesting.GetActionImpl{
					ActionImpl: clienttesting.ActionImpl{
						Verb:     "get",
						Resource: gvr("group", "version", "resources"),
					},
					Name: "aThing",
				},
			},
			expectedError: `Operation cannot be fulfilled on resources.group "aThing": the resource has been modified in the meantime`,
		},
		{
			name:          "Remove syncer finalizer - success",
			gvr:           gvr("group", "version", "resources"),
			synctargetKey: "syncTargetKey",
			availableResources: []runtime.Object{
				resource("group/version", "Resource", "aThing").
					finalizer("workload.kcp.dev/syncer-syncTargetKey").
					label("state.workload.kcp.dev/syncTargetKey", "Sync").
					annotation("deletion.internal.workload.kcp.dev/syncTargetKey", deletionTimestamp.Format(time.RFC3339))(),
			},
			action: func(ctx context.Context, transformingClient dynamic.NamespaceableResourceInterface) (result interface{}, err error) {
				return transformingClient.Update(ctx, resource("group/version", "Resource", "aThing").
					deletionTimestamp(&deletionTimestamp)(), metav1.UpdateOptions{})
			},
			transform: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "value", "added")
				return result, nil
			},
			fieldsToSummarize: []field{{FieldPath: "status"}, {FieldPath: "spec.field"}},
			expectedClientActions: []clienttesting.Action{
				clienttesting.GetActionImpl{
					ActionImpl: clienttesting.ActionImpl{
						Verb:     "get",
						Resource: gvr("group", "version", "resources"),
					},
					Name: "aThing",
				},
				clienttesting.UpdateActionImpl{
					ActionImpl: clienttesting.ActionImpl{
						Verb:     "update",
						Resource: gvr("group", "version", "resources"),
					},
					Object: resource("group/version", "Resource", "aThing").finalizers().labels().annotations()(),
				},
			},
			expectedResult: resource("group/version", "Resource", "aThing").labels().annotations().
				field("added", "value")(),
		},
		{
			name:          "Remove syncer finalizer - should promote remaining synctarget status - success",
			gvr:           gvr("group", "version", "resources"),
			synctargetKey: "syncTargetKey",
			availableResources: []runtime.Object{
				resource("group/version", "Resource", "aThing").
					finalizer("workload.kcp.dev/syncer-syncTargetKey").
					finalizer("workload.kcp.dev/syncer-syncTargetKey2").
					label("state.workload.kcp.dev/syncTargetKey", "Sync").
					label("state.workload.kcp.dev/syncTargetKey2", "Sync").
					annotation("diff.syncer.internal.kcp.dev/syncTargetKey", `{"status":{"statusField":"updated"}}`).
					annotation("diff.syncer.internal.kcp.dev/syncTargetKey2", `{"status":{"statusField":"new"}}`).
					annotation("deletion.internal.workload.kcp.dev/syncTargetKey", deletionTimestamp.Format(time.RFC3339))(),
			},
			action: func(ctx context.Context, transformingClient dynamic.NamespaceableResourceInterface) (result interface{}, err error) {
				return transformingClient.Update(ctx, resource("group/version", "Resource", "aThing").
					annotation("deletion.internal.workload.kcp.dev/syncTargetKey", deletionTimestamp.Format(time.RFC3339)).
					deletionTimestamp(&deletionTimestamp)(), metav1.UpdateOptions{})
			},
			transform: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "value", "added")
				return result, nil
			},
			fieldsToSummarize: []field{{FieldPath: "status", PromoteToUpstream: true}},
			expectedClientActions: []clienttesting.Action{
				clienttesting.GetActionImpl{
					ActionImpl: clienttesting.ActionImpl{
						Verb:     "get",
						Resource: gvr("group", "version", "resources"),
					},
					Name: "aThing",
				},
				clienttesting.UpdateActionImpl{
					ActionImpl: clienttesting.ActionImpl{
						Verb:        "update",
						Resource:    gvr("group", "version", "resources"),
						Subresource: "status",
					},
					Object: resource("group/version", "Resource", "aThing").
						finalizer("workload.kcp.dev/syncer-syncTargetKey").
						finalizer("workload.kcp.dev/syncer-syncTargetKey2").
						label("state.workload.kcp.dev/syncTargetKey", "Sync").
						label("state.workload.kcp.dev/syncTargetKey2", "Sync").
						annotation("deletion.internal.workload.kcp.dev/syncTargetKey", deletionTimestamp.Format(time.RFC3339)).
						annotation("diff.syncer.internal.kcp.dev/syncTargetKey", `{"status":{"statusField":"updated"}}`).
						annotation("diff.syncer.internal.kcp.dev/syncTargetKey2", `{"status":"##promoted##"}`).
						field("status", map[string]any{"statusField": string("new")})(),
				},
				clienttesting.UpdateActionImpl{
					ActionImpl: clienttesting.ActionImpl{
						Verb:     "update",
						Resource: gvr("group", "version", "resources"),
					},
					Object: resource("group/version", "Resource", "aThing").
						finalizer("workload.kcp.dev/syncer-syncTargetKey2").
						label("state.workload.kcp.dev/syncTargetKey2", "Sync").
						annotation("diff.syncer.internal.kcp.dev/syncTargetKey2", `{"status":"##promoted##"}`).
						field("status", map[string]any{"statusField": string("new")})(),
				},
			},
			expectedResult: resource("group/version", "Resource", "aThing").labels().annotations().
				label("state.workload.kcp.dev/syncTargetKey2", "Sync").
				field("added", "value")(),
		},
		{
			name: "watch - success",
			gvr:  schema.GroupVersionResource{Group: "group", Version: "version", Resource: "resources"},
			action: func(ctx context.Context, transformingClient dynamic.NamespaceableResourceInterface) (result interface{}, err error) {
				return transformingClient.Watch(ctx, metav1.ListOptions{})
			},
			transform: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added "+result.GetName(), "after")
				return result, nil
			},
			// add fields here to see how they are overridden from the diff annotation
			expectedClientActions: []clienttesting.Action{
				clienttesting.WatchActionImpl{
					ActionImpl: clienttesting.ActionImpl{
						Verb: "watch",
						Resource: schema.GroupVersionResource{
							Group: "group", Version: "version", Resource: "resources",
						},
					},
					WatchRestrictions: watchRestrictionsFromListOptions(metav1.ListOptions{}),
				},
			},
			checkResult: func(t *testing.T, watchTester *watch.FakeWatcher, result interface{}) {
				watcher, ok := result.(watch.Interface)
				if !ok {
					require.Fail(t, "result of Watch should be a watch.Interface")
				}

				watchedError := &metav1.Status{
					Status:  "Failure",
					Message: "message",
				}
				checkWatchEvents(t,
					watcher,
					func() {
						watchTester.Add(&unstructured.Unstructured{
							Object: map[string]interface{}{
								"apiVersion": "group/version",
								"kind":       "Resource",
								"metadata": map[string]interface{}{
									"name": "aThing",
								},
							},
						})
						watchTester.Add(&unstructured.Unstructured{
							Object: map[string]interface{}{
								"apiVersion": "group/version",
								"kind":       "Resource",
								"metadata": map[string]interface{}{
									"name": "aThingMore",
								},
							},
						})
						watchTester.Modify(&unstructured.Unstructured{
							Object: map[string]interface{}{
								"apiVersion": "group/version",
								"kind":       "Resource",
								"metadata": map[string]interface{}{
									"name": "aThing",
								},
							},
						})
						watchTester.Delete(&unstructured.Unstructured{
							Object: map[string]interface{}{
								"apiVersion": "group/version",
								"kind":       "Resource",
								"metadata": map[string]interface{}{
									"name": "aThingMore",
								},
							},
						})
						watchTester.Error(watchedError)
					},
					[]watch.Event{
						{Type: watch.Added, Object: &unstructured.Unstructured{
							Object: map[string]interface{}{
								"apiVersion": "group/version",
								"kind":       "Resource",
								"metadata": map[string]interface{}{
									"name": "aThing",
								},
								"after": "added aThing",
							},
						}},
						{Type: watch.Added, Object: &unstructured.Unstructured{
							Object: map[string]interface{}{
								"apiVersion": "group/version",
								"kind":       "Resource",
								"metadata": map[string]interface{}{
									"name": "aThingMore",
								},
								"after": "added aThingMore",
							},
						}},
						{Type: watch.Modified, Object: &unstructured.Unstructured{
							Object: map[string]interface{}{
								"apiVersion": "group/version",
								"kind":       "Resource",
								"metadata": map[string]interface{}{
									"name": "aThing",
								},
								"after": "added aThing",
							},
						}},
						{Type: watch.Deleted, Object: &unstructured.Unstructured{
							Object: map[string]interface{}{
								"apiVersion": "group/version",
								"kind":       "Resource",
								"metadata": map[string]interface{}{
									"name": "aThingMore",
								},
								"after": "added aThingMore",
							},
						}},
						{Type: watch.Error, Object: watchedError},
					})
			},
		},
	}

	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			fakeClient := fake.NewSimpleDynamicClient(runtime.NewScheme(), test.availableResources...)
			var lclustersRequestedInActions []string
			clusterClient := &mockedClusterClient{
				client: fakeClient,
				lclusterRecorder: func(lcluster string) {
					lclustersRequestedInActions = append(lclustersRequestedInActions, lcluster)
				},
			}

			rt := &SyncerResourceTransformer{}
			if test.transform != nil {
				rt.TransformationProvider = &mockedTransformation{
					test.transform,
				}
			}
			if test.fieldsToSummarize != nil {
				rt.SummarizingRulesProvider = &mockedSummarizingRules{
					test.fieldsToSummarize,
				}
			}

			fakeWatcher := watch.NewFake()
			defer fakeWatcher.Stop()
			fakeClient.PrependWatchReactor("resources", clienttesting.DefaultWatchReactor(fakeWatcher, nil))

			transformingClient := transforming.WithResourceTransformer(clusterClient, rt)
			ctx := syncercontext.WithSyncTargetKey(context.Background(), test.synctargetKey)
			ctx = dynamiccontext.WithAPIDomainKey(ctx, dynamiccontext.APIDomainKey(client.ToClusterAwareKey(logicalcluster.NewPath("root:negotiation"), "SyncTargetName")))
			result, err := test.action(ctx, transformingClient.Cluster(logicalcluster.NewPath("")).Resource(test.gvr))

			if test.expectedError != "" {
				require.EqualError(t, err, test.expectedError, "error is wrong")
			} else {
				require.NoError(t, err, "error is wrong")
			}

			if test.checkResult != nil {
				test.checkResult(t, fakeWatcher, result)
			} else if test.expectedResult != nil {
				require.Empty(t, cmp.Diff(test.expectedResult, result, cmpopts.SortSlices(sortUnstructured)), "result is wrong")
			} else {
				require.Nil(t, result, "result is wrong")
			}
			require.Empty(t, cmp.Diff(test.expectedClientActions, fakeClient.Actions()), "client actions are wrong")
		})
	}
}

func sortUnstructured(a *unstructured.Unstructured, b *unstructured.Unstructured) bool {
	return a.GetName() > b.GetName()
}

func watchRestrictionsFromListOptions(options metav1.ListOptions) clienttesting.WatchRestrictions {
	labelSelector, fieldSelector, _ := clienttesting.ExtractFromListOptions(options)
	return clienttesting.WatchRestrictions{
		Labels: labelSelector,
		Fields: fieldSelector,
	}
}

func checkWatchEvents(t *testing.T, watcher watch.Interface, addEvents func(), expectedEvents []watch.Event) {
	watchingStarted := make(chan bool, 1)
	go func() {
		<-watchingStarted
		addEvents()
	}()

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
		require.True(t, equality.Semantic.DeepEqual(expectedEvent.Object, event.Object), cmp.Diff(expectedEvent.Object, event.Object))
	}
}
