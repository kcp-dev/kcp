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

package transforming

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/api/equality"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
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

	clientdynamic "github.com/kcp-dev/kcp/pkg/virtual/framework/client/dynamic"
)

type clustered interface {
	ClusterName() string
}

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

func (c *mockedClusterClient) Cluster(cluster logicalcluster.Name) dynamic.Interface {
	return &dynamicClient{
		client:           c.client,
		lcluster:         cluster,
		lclusterRecorder: c.lclusterRecorder,
	}
}

type mockedResourceClusterClient struct {
	resourceClient
}

func (c *mockedResourceClusterClient) Cluster(lcluster logicalcluster.Name) dynamic.NamespaceableResourceInterface {
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
	lcluster         logicalcluster.Name
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
	lcluster          logicalcluster.Name
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

type transformerCall struct {
	Moment       string
	GVR          schema.GroupVersionResource
	Resource     *unstructured.Unstructured
	SubResources []string
	LCluster     string
	EventType    string
}

type resourceTransformer struct {
	after  func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error)
	before func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error)
	calls  []transformerCall
}

func (rt *resourceTransformer) BeforeWrite(client dynamic.ResourceInterface, ctx context.Context, gvr schema.GroupVersionResource, resource *unstructured.Unstructured, subresources ...string) (transformed *unstructured.Unstructured, err error) {
	lcluster := client.(clustered).ClusterName()
	rt.calls = append(rt.calls, transformerCall{
		Moment:       "before",
		GVR:          gvr,
		Resource:     resource,
		SubResources: subresources,
		LCluster:     lcluster,
	})
	return rt.before(resource)
}

func (rt *resourceTransformer) AfterRead(client dynamic.ResourceInterface, ctx context.Context, gvr schema.GroupVersionResource, resource *unstructured.Unstructured, eventType *watch.EventType, subresources ...string) (transformed *unstructured.Unstructured, err error) {
	lcluster := client.(clustered).ClusterName()
	var eventTypeStr string
	if eventType != nil {
		eventTypeStr = string(*eventType)
	}
	rt.calls = append(rt.calls, transformerCall{
		Moment:       "after",
		GVR:          gvr,
		Resource:     resource,
		SubResources: subresources,
		LCluster:     lcluster,
		EventType:    eventTypeStr,
	})
	return rt.after(resource)
}

func gvr(group, version, resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    group,
		Version:  version,
		Resource: resource,
	}
}

type resourceBuilder func() *unstructured.Unstructured

func (rb resourceBuilder) ns(namespace string) resourceBuilder {
	return func() *unstructured.Unstructured {
		r := rb()
		r.Object["metadata"].(map[string]interface{})["namespace"] = namespace
		return r
	}
}

func (rb resourceBuilder) cluster(cluster string) resourceBuilder {
	return func() *unstructured.Unstructured {
		r := rb()
		metadata := r.Object["metadata"].(map[string]interface{})
		if _, exists := metadata["annotations"]; !exists {
			metadata["annotations"] = map[string]interface{}{}

		}
		metadata["annotations"].(map[string]interface{})["kcp.dev/cluster"] = cluster
		return r
	}
}

func (rb resourceBuilder) field(name, value string) resourceBuilder {
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

func TestResourceTransformer(t *testing.T) {
	testCases := []struct {
		name                        string
		lcluster                    string
		gvr                         schema.GroupVersionResource
		availableResources          []runtime.Object
		action                      func(ctx context.Context, transformingClient kcpdynamic.ClusterInterface, lcluster logicalcluster.Name, gvr schema.GroupVersionResource) (result interface{}, err error)
		after                       func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error)
		before                      func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error)
		expectedResult              interface{}
		checkResult                 func(t *testing.T, watchTester *watch.FakeWatcher, result interface{})
		expectedError               string
		expectedClientAction        clienttesting.Action
		expectedClientActionCluster string
		expectedTransformerCalls    []transformerCall
	}{
		{
			name:     "create - success",
			lcluster: "cluster1",
			gvr:      gvr("group", "version", "resources"),
			action: func(ctx context.Context, transformingClient kcpdynamic.ClusterInterface, lcluster logicalcluster.Name, gvr schema.GroupVersionResource) (result interface{}, err error) {
				return transformingClient.Cluster(lcluster).Resource(gvr).Create(ctx, resource("group/version", "Resource", "aThing")(), metav1.CreateOptions{})
			},
			before: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added", "before")
				return result, nil
			},
			after: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added", "after")
				return result, nil
			},
			expectedClientAction: clienttesting.CreateActionImpl{
				ActionImpl: clienttesting.ActionImpl{
					Verb:     "create",
					Resource: gvr("group", "version", "resources"),
				},
				Object: resource("group/version", "Resource", "aThing").field("before", "added")(),
			},
			expectedClientActionCluster: "cluster1",
			expectedResult:              resource("group/version", "Resource", "aThing").field("before", "added").field("after", "added")(),
			expectedTransformerCalls: []transformerCall{
				{
					Moment:   "before",
					GVR:      gvr("group", "version", "resources"),
					Resource: resource("group/version", "Resource", "aThing")(),
					LCluster: "cluster1",
				},
				{
					Moment:   "after",
					GVR:      gvr("group", "version", "resources"),
					Resource: resource("group/version", "Resource", "aThing").field("before", "added")(),
					LCluster: "cluster1",
				},
			},
		},
		{
			name:     "create - already exists",
			lcluster: "cluster1",
			gvr:      gvr("group", "version", "resources"),
			availableResources: []runtime.Object{
				resource("group/version", "Resource", "aThing")(),
			},
			action: func(ctx context.Context, transformingClient kcpdynamic.ClusterInterface, lcluster logicalcluster.Name, gvr schema.GroupVersionResource) (result interface{}, err error) {
				return transformingClient.Resource(gvr).Cluster(lcluster).Create(ctx, resource("group/version", "Resource", "aThing")(), metav1.CreateOptions{})
			},
			before: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added", "before")
				return result, nil
			},
			after: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added", "after")
				return result, nil
			},
			expectedClientAction: clienttesting.CreateActionImpl{
				ActionImpl: clienttesting.ActionImpl{
					Verb:     "create",
					Resource: gvr("group", "version", "resources"),
				},
				Object: resource("group/version", "Resource", "aThing").field("before", "added")(),
			},
			expectedClientActionCluster: "cluster1",
			expectedError:               `resources.group "aThing" already exists`,
			expectedTransformerCalls: []transformerCall{
				{
					Moment:   "before",
					GVR:      gvr("group", "version", "resources"),
					Resource: resource("group/version", "Resource", "aThing")(),
					LCluster: "cluster1",
				},
			},
		},
		{
			name:     "update - success",
			lcluster: "cluster1",
			gvr:      gvr("group", "version", "resources"),
			availableResources: []runtime.Object{
				resource("group/version", "Resource", "aThing")(),
			},
			action: func(ctx context.Context, transformingClient kcpdynamic.ClusterInterface, lcluster logicalcluster.Name, gvr schema.GroupVersionResource) (result interface{}, err error) {
				return transformingClient.Resource(gvr).Cluster(lcluster).Update(ctx, resource("group/version", "Resource", "aThing")(), metav1.UpdateOptions{})
			},
			before: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added", "before")
				return result, nil
			},
			after: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added", "after")
				return result, nil
			},
			expectedClientAction: clienttesting.UpdateActionImpl{
				ActionImpl: clienttesting.ActionImpl{
					Verb: "update",
					Resource: schema.GroupVersionResource{
						Group: "group", Version: "version", Resource: "resources",
					},
				},
				Object: resource("group/version", "Resource", "aThing").field("before", "added")(),
			},
			expectedClientActionCluster: "cluster1",
			expectedResult:              resource("group/version", "Resource", "aThing").field("before", "added").field("after", "added")(),
			expectedTransformerCalls: []transformerCall{
				{
					Moment:   "before",
					GVR:      gvr("group", "version", "resources"),
					Resource: resource("group/version", "Resource", "aThing")(),
					LCluster: "cluster1",
				},
				{
					Moment:   "after",
					GVR:      gvr("group", "version", "resources"),
					Resource: resource("group/version", "Resource", "aThing").field("before", "added")(),
					LCluster: "cluster1",
				},
			},
		},
		{
			name:     "update status - success",
			lcluster: "cluster1",
			gvr:      gvr("group", "version", "resources"),
			availableResources: []runtime.Object{
				resource("group/version", "Resource", "aThing")(),
			},
			action: func(ctx context.Context, transformingClient kcpdynamic.ClusterInterface, lcluster logicalcluster.Name, gvr schema.GroupVersionResource) (result interface{}, err error) {
				return transformingClient.Resource(gvr).Cluster(lcluster).UpdateStatus(ctx, resource("group/version", "Resource", "aThing")(), metav1.UpdateOptions{})
			},
			before: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added", "before")
				return result, nil
			},
			after: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added", "after")
				return result, nil
			},
			expectedClientAction: clienttesting.UpdateActionImpl{
				ActionImpl: clienttesting.ActionImpl{
					Verb: "update",
					Resource: schema.GroupVersionResource{
						Group: "group", Version: "version", Resource: "resources",
					},
					Subresource: "status",
				},
				Object: resource("group/version", "Resource", "aThing").field("before", "added")(),
			},
			expectedClientActionCluster: "cluster1",
			expectedResult:              resource("group/version", "Resource", "aThing").field("before", "added").field("after", "added")(),
			expectedTransformerCalls: []transformerCall{
				{
					Moment:       "before",
					GVR:          gvr("group", "version", "resources"),
					Resource:     resource("group/version", "Resource", "aThing")(),
					LCluster:     "cluster1",
					SubResources: []string{"status"},
				},
				{
					Moment:       "after",
					GVR:          gvr("group", "version", "resources"),
					Resource:     resource("group/version", "Resource", "aThing").field("before", "added")(),
					LCluster:     "cluster1",
					SubResources: []string{"status"},
				},
			},
		},
		{
			name:     "update - not found",
			lcluster: "cluster1",
			gvr:      gvr("group", "version", "resources"),
			action: func(ctx context.Context, transformingClient kcpdynamic.ClusterInterface, lcluster logicalcluster.Name, gvr schema.GroupVersionResource) (result interface{}, err error) {
				return transformingClient.Resource(gvr).Cluster(lcluster).Update(ctx, resource("group/version", "Resource", "aThing")(), metav1.UpdateOptions{})
			},
			before: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added", "before")
				return result, nil
			},
			after: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added", "after")
				return result, nil
			},
			expectedClientAction: clienttesting.UpdateActionImpl{
				ActionImpl: clienttesting.ActionImpl{
					Verb:     "update",
					Resource: gvr("group", "version", "resources"),
				},
				Object: resource("group/version", "Resource", "aThing").field("before", "added")(),
			},
			expectedClientActionCluster: "cluster1",
			expectedError:               `resources.group "aThing" not found`,
			expectedTransformerCalls: []transformerCall{
				{
					Moment:   "before",
					GVR:      gvr("group", "version", "resources"),
					Resource: resource("group/version", "Resource", "aThing")(),
					LCluster: "cluster1",
				},
			},
		},
		{
			name:     "get - success",
			lcluster: "cluster1",
			gvr:      gvr("group", "version", "resources"),
			availableResources: []runtime.Object{
				resource("group/version", "Resource", "aThing")(),
			},
			action: func(ctx context.Context, transformingClient kcpdynamic.ClusterInterface, lcluster logicalcluster.Name, gvr schema.GroupVersionResource) (result interface{}, err error) {
				return transformingClient.Resource(gvr).Cluster(lcluster).Get(ctx, "aThing", metav1.GetOptions{})
			},
			after: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added", "after")
				return result, nil
			},
			expectedClientAction: clienttesting.GetActionImpl{
				ActionImpl: clienttesting.ActionImpl{
					Verb:     "get",
					Resource: gvr("group", "version", "resources"),
				},
				Name: "aThing",
			},
			expectedClientActionCluster: "cluster1",
			expectedResult:              resource("group/version", "Resource", "aThing").field("after", "added")(),
			expectedTransformerCalls: []transformerCall{
				{
					Moment:   "after",
					GVR:      gvr("group", "version", "resources"),
					Resource: resource("group/version", "Resource", "aThing")(),
					LCluster: "cluster1",
				},
			},
		},
		{
			name:     "get - not found",
			lcluster: "cluster1",
			gvr:      gvr("group", "version", "resources"),
			action: func(ctx context.Context, transformingClient kcpdynamic.ClusterInterface, lcluster logicalcluster.Name, gvr schema.GroupVersionResource) (result interface{}, err error) {
				return transformingClient.Resource(gvr).Cluster(lcluster).Get(ctx, "aThing", metav1.GetOptions{})
			},
			after: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added", "after")
				return result, nil
			},
			expectedClientAction: clienttesting.GetActionImpl{
				ActionImpl: clienttesting.ActionImpl{
					Verb:     "get",
					Resource: gvr("group", "version", "resources"),
				},
				Name: "aThing",
			},
			expectedClientActionCluster: "cluster1",
			expectedError:               `resources.group "aThing" not found`,
		},
		{
			name: "list cross cluster - success",
			gvr:  gvr("group", "version", "resources"),
			availableResources: []runtime.Object{
				resource("group/version", "Resource", "aThing").cluster("cluster1")(),
				resource("group/version", "Resource", "aThingMore").cluster("cluster2")(),
			},
			action: func(ctx context.Context, transformingClient kcpdynamic.ClusterInterface, lcluster logicalcluster.Name, gvr schema.GroupVersionResource) (result interface{}, err error) {
				return transformingClient.Resource(gvr).List(ctx, metav1.ListOptions{})
			},
			after: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added "+result.GetName(), "after")
				return result, nil
			},
			expectedClientAction: clienttesting.ListActionImpl{
				ActionImpl: clienttesting.ActionImpl{
					Verb:     "list",
					Resource: gvr("group", "version", "resources"),
				},
				Kind:             schema.GroupVersionKind{Group: "group", Version: "version", Kind: "Resource"},
				ListRestrictions: listRestrictionsFromListOptions(metav1.ListOptions{}),
			},
			expectedClientActionCluster: "",
			expectedResult: &unstructured.UnstructuredList{
				Object: map[string]interface{}{
					"apiVersion": "group/version",
					"kind":       "ResourceList",
					"metadata":   map[string]interface{}{"resourceVersion": ""},
				},
				Items: []unstructured.Unstructured{
					*resource("group/version", "Resource", "aThing").cluster("cluster1").field("after", "added aThing")(),
					*resource("group/version", "Resource", "aThingMore").cluster("cluster2").field("after", "added aThingMore")(),
				},
			},
			expectedTransformerCalls: []transformerCall{
				{
					Moment:   "after",
					GVR:      gvr("group", "version", "resources"),
					Resource: resource("group/version", "Resource", "aThing").cluster("cluster1")(),
					LCluster: "cluster1",
				},
				{
					Moment:   "after",
					GVR:      gvr("group", "version", "resources"),
					Resource: resource("group/version", "Resource", "aThingMore").cluster("cluster2")(),
					LCluster: "cluster2",
				},
			},
		},
		{
			name: "list cross cluster - success with filtering",
			gvr:  gvr("group", "version", "resources"),
			availableResources: []runtime.Object{
				resource("group/version", "Resource", "aThing").cluster("cluster1")(),
				resource("group/version", "Resource", "aThingMore").cluster("cluster2")(),
			},
			action: func(ctx context.Context, transformingClient kcpdynamic.ClusterInterface, lcluster logicalcluster.Name, gvr schema.GroupVersionResource) (result interface{}, err error) {
				return transformingClient.Resource(gvr).List(ctx, metav1.ListOptions{})
			},
			after: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				if resource.GetName() == "aThing" {
					return nil, kerrors.NewNotFound(schema.GroupResource{Group: "group", Resource: "resources"}, "aThing")
				}
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added "+result.GetName(), "after")
				return result, nil
			},
			expectedClientAction: clienttesting.ListActionImpl{
				ActionImpl: clienttesting.ActionImpl{
					Verb:     "list",
					Resource: gvr("group", "version", "resources"),
				},
				Kind:             schema.GroupVersionKind{Group: "group", Version: "version", Kind: "Resource"},
				ListRestrictions: listRestrictionsFromListOptions(metav1.ListOptions{}),
			},
			expectedClientActionCluster: "",
			expectedResult: &unstructured.UnstructuredList{
				Object: map[string]interface{}{
					"apiVersion": "group/version",
					"kind":       "ResourceList",
					"metadata":   map[string]interface{}{"resourceVersion": ""},
				},
				Items: []unstructured.Unstructured{
					*resource("group/version", "Resource", "aThingMore").cluster("cluster2").field("after", "added aThingMore")(),
				},
			},
			expectedTransformerCalls: []transformerCall{
				{
					Moment:   "after",
					GVR:      gvr("group", "version", "resources"),
					Resource: resource("group/version", "Resource", "aThing").cluster("cluster1")(),
					LCluster: "cluster1",
				},
				{
					Moment:   "after",
					GVR:      gvr("group", "version", "resources"),
					Resource: resource("group/version", "Resource", "aThingMore").cluster("cluster2")(),
					LCluster: "cluster2",
				},
			},
		},
		{
			name:     "list in cluster - success",
			lcluster: "cluster1",
			gvr:      gvr("group", "version", "resources"),
			availableResources: []runtime.Object{
				resource("group/version", "Resource", "aThing")(),
				resource("group/version", "Resource", "aThingMore")(),
			},
			action: func(ctx context.Context, transformingClient kcpdynamic.ClusterInterface, lcluster logicalcluster.Name, gvr schema.GroupVersionResource) (result interface{}, err error) {
				return transformingClient.Resource(gvr).Cluster(lcluster).List(ctx, metav1.ListOptions{})
			},
			after: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added "+result.GetName(), "after")
				return result, nil
			},
			expectedClientAction: clienttesting.ListActionImpl{
				ActionImpl: clienttesting.ActionImpl{
					Verb:     "list",
					Resource: gvr("group", "version", "resources"),
				},
				Kind:             schema.GroupVersionKind{Group: "group", Version: "version", Kind: "Resource"},
				ListRestrictions: listRestrictionsFromListOptions(metav1.ListOptions{}),
			},
			expectedClientActionCluster: "cluster1",
			expectedResult: &unstructured.UnstructuredList{
				Object: map[string]interface{}{
					"apiVersion": "group/version",
					"kind":       "ResourceList",
					"metadata":   map[string]interface{}{"resourceVersion": ""},
				},
				Items: []unstructured.Unstructured{
					*resource("group/version", "Resource", "aThing").field("after", "added aThing")(),
					*resource("group/version", "Resource", "aThingMore").field("after", "added aThingMore")(),
				},
			},
			expectedTransformerCalls: []transformerCall{
				{
					Moment:   "after",
					GVR:      gvr("group", "version", "resources"),
					Resource: resource("group/version", "Resource", "aThing")(),
					LCluster: "cluster1",
				},
				{
					Moment:   "after",
					GVR:      gvr("group", "version", "resources"),
					Resource: resource("group/version", "Resource", "aThingMore")(),
					LCluster: "cluster1",
				},
			},
		},
		{
			name:     "list in cluster - success - old way",
			lcluster: "cluster1",
			gvr:      gvr("group", "version", "resources"),
			availableResources: []runtime.Object{
				resource("group/version", "Resource", "aThing")(),
				resource("group/version", "Resource", "aThingMore")(),
			},
			action: func(ctx context.Context, transformingClient kcpdynamic.ClusterInterface, lcluster logicalcluster.Name, gvr schema.GroupVersionResource) (result interface{}, err error) {
				return transformingClient.Cluster(lcluster).Resource(gvr).List(ctx, metav1.ListOptions{})
			},
			after: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added "+result.GetName(), "after")
				return result, nil
			},
			expectedClientAction: clienttesting.ListActionImpl{
				ActionImpl: clienttesting.ActionImpl{
					Verb:     "list",
					Resource: gvr("group", "version", "resources"),
				},
				Kind:             schema.GroupVersionKind{Group: "group", Version: "version", Kind: "Resource"},
				ListRestrictions: listRestrictionsFromListOptions(metav1.ListOptions{}),
			},
			expectedClientActionCluster: "cluster1",
			expectedResult: &unstructured.UnstructuredList{
				Object: map[string]interface{}{
					"apiVersion": "group/version",
					"kind":       "ResourceList",
					"metadata":   map[string]interface{}{"resourceVersion": ""},
				},
				Items: []unstructured.Unstructured{
					*resource("group/version", "Resource", "aThing").field("after", "added aThing")(),
					*resource("group/version", "Resource", "aThingMore").field("after", "added aThingMore")(),
				},
			},
			expectedTransformerCalls: []transformerCall{
				{
					Moment:   "after",
					GVR:      gvr("group", "version", "resources"),
					Resource: resource("group/version", "Resource", "aThing")(),
					LCluster: "cluster1",
				},
				{
					Moment:   "after",
					GVR:      gvr("group", "version", "resources"),
					Resource: resource("group/version", "Resource", "aThingMore")(),
					LCluster: "cluster1",
				},
			},
		},
		{
			name:     "list in namespace - success",
			lcluster: "cluster1",
			gvr:      gvr("group", "version", "resources"),
			availableResources: []runtime.Object{
				resource("group/version", "Resource", "aThing").ns("aNamespace")(),
				resource("group/version", "Resource", "aThingMore").ns("aNamespace")(),
				resource("group/version", "Resource", "aThingIgnored").ns("aDistinctNamespace")(),
			},
			action: func(ctx context.Context, transformingClient kcpdynamic.ClusterInterface, lcluster logicalcluster.Name, gvr schema.GroupVersionResource) (result interface{}, err error) {
				return transformingClient.Resource(gvr).Cluster(lcluster).Namespace("aNamespace").List(ctx, metav1.ListOptions{})
			},
			after: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added "+result.GetName(), "after")
				return result, nil
			},
			expectedClientAction: clienttesting.ListActionImpl{
				ActionImpl: clienttesting.ActionImpl{
					Verb:      "list",
					Resource:  gvr("group", "version", "resources"),
					Namespace: "aNamespace",
				},
				Kind:             schema.GroupVersionKind{Group: "group", Version: "version", Kind: "Resource"},
				ListRestrictions: listRestrictionsFromListOptions(metav1.ListOptions{}),
			},
			expectedClientActionCluster: "cluster1",
			expectedResult: &unstructured.UnstructuredList{
				Object: map[string]interface{}{
					"apiVersion": "group/version",
					"kind":       "ResourceList",
					"metadata":   map[string]interface{}{"resourceVersion": ""},
				},
				Items: []unstructured.Unstructured{
					*resource("group/version", "Resource", "aThing").ns("aNamespace").field("after", "added aThing")(),
					*resource("group/version", "Resource", "aThingMore").ns("aNamespace").field("after", "added aThingMore")(),
				},
			},
			expectedTransformerCalls: []transformerCall{
				{
					Moment:   "after",
					GVR:      gvr("group", "version", "resources"),
					Resource: resource("group/version", "Resource", "aThing").ns("aNamespace")(),
					LCluster: "cluster1",
				},
				{
					Moment:   "after",
					GVR:      gvr("group", "version", "resources"),
					Resource: resource("group/version", "Resource", "aThingMore").ns("aNamespace")(),
					LCluster: "cluster1",
				},
			},
		},
		{
			name:     "delete - success",
			lcluster: "cluster1",
			gvr:      gvr("group", "version", "resources"),
			availableResources: []runtime.Object{
				resource("group/version", "Resource", "aThing")(),
			},
			action: func(ctx context.Context, transformingClient kcpdynamic.ClusterInterface, lcluster logicalcluster.Name, gvr schema.GroupVersionResource) (result interface{}, err error) {
				return nil, transformingClient.Resource(gvr).Cluster(lcluster).Delete(context.Background(), "aThing", metav1.DeleteOptions{}, "")
			},
			expectedClientAction: clienttesting.DeleteActionImpl{
				ActionImpl: clienttesting.ActionImpl{
					Verb:     "delete",
					Resource: gvr("group", "version", "resources"),
				},
				Name: "aThing",
			},
			expectedClientActionCluster: "cluster1",
		},
		{
			name:     "delete - notfound",
			lcluster: "cluster1",
			gvr:      gvr("group", "version", "resources"),
			action: func(ctx context.Context, transformingClient kcpdynamic.ClusterInterface, lcluster logicalcluster.Name, gvr schema.GroupVersionResource) (result interface{}, err error) {
				return nil, transformingClient.Resource(gvr).Cluster(lcluster).Namespace("aNamespace").Delete(ctx, "aThing", metav1.DeleteOptions{})
			},
			expectedClientAction: clienttesting.DeleteActionImpl{
				ActionImpl: clienttesting.ActionImpl{
					Verb:      "delete",
					Resource:  gvr("group", "version", "resources"),
					Namespace: "aNamespace",
				},
				Name: "aThing",
			},
			expectedClientActionCluster: "cluster1",
			expectedError:               `resources.group "aThing" not found`,
		},
		{
			name:     "delete with result - success",
			lcluster: "cluster1",
			gvr:      gvr("group", "version", "resources"),
			availableResources: []runtime.Object{
				resource("group/version", "Resource", "aThing")(),
			},
			action: func(ctx context.Context, transformingClient kcpdynamic.ClusterInterface, lcluster logicalcluster.Name, gvr schema.GroupVersionResource) (result interface{}, err error) {
				deleterWithResult, _ := transformingClient.Resource(gvr).Cluster(lcluster).(clientdynamic.DeleterWithResults)
				deleteresult, _, err := deleterWithResult.DeleteWithResult(context.Background(), "aThing", metav1.DeleteOptions{})
				return deleteresult, err
			},
			after: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added", "after")
				return result, nil
			},
			expectedClientAction: clienttesting.DeleteActionImpl{
				ActionImpl: clienttesting.ActionImpl{
					Verb:     "delete",
					Resource: gvr("group", "version", "resources"),
				},
				Name: "aThing",
			},
			expectedClientActionCluster: "cluster1",
			expectedResult:              resource("group/version", "Resource", "aThing").field("after", "added")(),
			expectedTransformerCalls: []transformerCall{
				{
					Moment:   "after",
					GVR:      gvr("group", "version", "resources"),
					Resource: resource("group/version", "Resource", "aThing")(),
					LCluster: "cluster1",
				},
			},
		},
		{
			name:     "delete with result - notfound",
			lcluster: "cluster1",
			gvr:      gvr("group", "version", "resources"),
			action: func(ctx context.Context, transformingClient kcpdynamic.ClusterInterface, lcluster logicalcluster.Name, gvr schema.GroupVersionResource) (result interface{}, err error) {
				deleterWithResult, _ := transformingClient.Resource(gvr).Cluster(lcluster).Namespace("aNamespace").(clientdynamic.DeleterWithResults)
				deleteresult, _, err := deleterWithResult.DeleteWithResult(context.Background(), "aThing", metav1.DeleteOptions{}, "")
				return deleteresult, err
			},
			expectedClientAction: clienttesting.DeleteActionImpl{
				ActionImpl: clienttesting.ActionImpl{
					Verb:      "delete",
					Resource:  gvr("group", "version", "resources"),
					Namespace: "aNamespace",
				},
				Name: "aThing",
			},
			expectedClientActionCluster: "cluster1",
			expectedError:               `resources.group "aThing" not found`,
		},
		{
			name:     "deletecollection - success",
			lcluster: "cluster1",
			gvr:      gvr("group", "version", "resources"),
			availableResources: []runtime.Object{
				resource("group/version", "Resource", "aThing")(),
				resource("group/version", "Resource", "aThingMore")(),
			},
			action: func(ctx context.Context, transformingClient kcpdynamic.ClusterInterface, lcluster logicalcluster.Name, gvr schema.GroupVersionResource) (result interface{}, err error) {
				return nil, transformingClient.Resource(gvr).Cluster(lcluster).DeleteCollection(context.Background(), metav1.DeleteOptions{}, metav1.ListOptions{})
			},
			expectedClientAction: clienttesting.DeleteCollectionActionImpl{
				ActionImpl: clienttesting.ActionImpl{
					Verb:     "delete-collection",
					Resource: gvr("group", "version", "resources"),
				},
				ListRestrictions: listRestrictionsFromListOptions(metav1.ListOptions{}),
			},
			expectedClientActionCluster: "cluster1",
		},
		{
			name:     "deletecollection with result - success",
			lcluster: "cluster1",
			gvr:      gvr("group", "version", "resources"),
			availableResources: []runtime.Object{
				resource("group/version", "Resource", "aThing")(),
				resource("group/version", "Resource", "aThingMore")(),
			},
			action: func(ctx context.Context, transformingClient kcpdynamic.ClusterInterface, lcluster logicalcluster.Name, gvr schema.GroupVersionResource) (result interface{}, err error) {
				deleterWithResult, _ := transformingClient.Resource(gvr).Cluster(lcluster).(clientdynamic.DeleterWithResults)
				deleteresult, err := deleterWithResult.DeleteCollectionWithResult(context.Background(), metav1.DeleteOptions{}, metav1.ListOptions{})
				return deleteresult, err
			},
			after: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added "+result.GetName(), "after")
				return result, nil
			},
			expectedClientAction: clienttesting.DeleteCollectionActionImpl{
				ActionImpl: clienttesting.ActionImpl{
					Verb:     "delete-collection",
					Resource: gvr("group", "version", "resources"),
				},
				ListRestrictions: listRestrictionsFromListOptions(metav1.ListOptions{}),
			},
			expectedClientActionCluster: "cluster1",
			expectedResult: &unstructured.UnstructuredList{
				Object: map[string]interface{}{
					"apiVersion": "group/version",
					"kind":       "ResourceList",
					"metadata":   map[string]interface{}{"resourceVersion": ""},
				},
				Items: []unstructured.Unstructured{
					*resource("group/version", "Resource", "aThing").field("after", "added aThing")(),
					*resource("group/version", "Resource", "aThingMore").field("after", "added aThingMore")(),
				},
			},
			expectedTransformerCalls: []transformerCall{
				{
					Moment:   "after",
					GVR:      gvr("group", "version", "resources"),
					Resource: resource("group/version", "Resource", "aThing")(),
					LCluster: "cluster1",
				},
				{
					Moment:   "after",
					GVR:      gvr("group", "version", "resources"),
					Resource: resource("group/version", "Resource", "aThingMore")(),
					LCluster: "cluster1",
				},
			},
		},
		{
			name: "patch - not implemented",
			gvr:  gvr("group", "version", "resources"),
			availableResources: []runtime.Object{
				&unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "group/version",
						"kind":       "Resource",
						"metadata": map[string]interface{}{
							"name": "aThing",
						},
					},
				},
			},
			action: func(ctx context.Context, transformingClient kcpdynamic.ClusterInterface, lcluster logicalcluster.Name, gvr schema.GroupVersionResource) (result interface{}, err error) {
				return transformingClient.Resource(gvr).Cluster(lcluster).Patch(context.Background(), "aThing", types.JSONPatchType, []byte(""), metav1.PatchOptions{})
			},
			expectedClientAction: nil,
			expectedError:        "not implemented",
		},
		{
			name: "watch cross cluster - success",
			gvr:  gvr("group", "version", "resources"),
			action: func(ctx context.Context, transformingClient kcpdynamic.ClusterInterface, lcluster logicalcluster.Name, gvr schema.GroupVersionResource) (result interface{}, err error) {
				return transformingClient.Resource(gvr).Watch(ctx, metav1.ListOptions{})
			},
			after: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added "+result.GetName(), "after")
				return result, nil
			},
			expectedClientAction: clienttesting.WatchActionImpl{
				ActionImpl: clienttesting.ActionImpl{
					Verb:     "watch",
					Resource: gvr("group", "version", "resources"),
				},
				WatchRestrictions: watchRestrictionsFromListOptions(metav1.ListOptions{}),
			},
			expectedClientActionCluster: "",
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
						watchTester.Add(resource("group/version", "Resource", "aThing").cluster("cluster1")())
						watchTester.Add(resource("group/version", "Resource", "aThingMore").cluster("cluster2")())
						watchTester.Modify(resource("group/version", "Resource", "aThing").cluster("cluster1")())
						watchTester.Delete(resource("group/version", "Resource", "aThingMore").cluster("cluster2")())
						watchTester.Error(watchedError)
					},
					[]watch.Event{
						{Type: watch.Added, Object: resource("group/version", "Resource", "aThing").cluster("cluster1").field("after", "added aThing")()},
						{Type: watch.Added, Object: resource("group/version", "Resource", "aThingMore").cluster("cluster2").field("after", "added aThingMore")()},
						{Type: watch.Modified, Object: resource("group/version", "Resource", "aThing").cluster("cluster1").field("after", "added aThing")()},
						{Type: watch.Deleted, Object: resource("group/version", "Resource", "aThingMore").cluster("cluster2").field("after", "added aThingMore")()},
						{Type: watch.Error, Object: watchedError},
					})
			},
			expectedTransformerCalls: []transformerCall{
				{
					Moment:    "after",
					GVR:       gvr("group", "version", "resources"),
					Resource:  resource("group/version", "Resource", "aThing").cluster("cluster1")(),
					LCluster:  "cluster1",
					EventType: "ADDED",
				},
				{
					Moment:    "after",
					GVR:       gvr("group", "version", "resources"),
					Resource:  resource("group/version", "Resource", "aThingMore").cluster("cluster2")(),
					LCluster:  "cluster2",
					EventType: "ADDED",
				},
				{
					Moment:    "after",
					GVR:       gvr("group", "version", "resources"),
					Resource:  resource("group/version", "Resource", "aThing").cluster("cluster1")(),
					LCluster:  "cluster1",
					EventType: "MODIFIED",
				},
				{
					Moment:    "after",
					GVR:       gvr("group", "version", "resources"),
					Resource:  resource("group/version", "Resource", "aThingMore").cluster("cluster2")(),
					LCluster:  "cluster2",
					EventType: "DELETED",
				},
			},
		},
		{
			name:     "watch in cluster - success with filtering",
			lcluster: "cluster1",
			gvr:      gvr("group", "version", "resources"),
			action: func(ctx context.Context, transformingClient kcpdynamic.ClusterInterface, lcluster logicalcluster.Name, gvr schema.GroupVersionResource) (result interface{}, err error) {
				return transformingClient.Resource(gvr).Cluster(lcluster).Watch(ctx, metav1.ListOptions{})
			},
			after: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				if resource.GetName() == "aThing" {
					return nil, kerrors.NewNotFound(schema.GroupResource{Group: "group", Resource: "resources"}, "aThing")
				}
				result := resource.DeepCopy()
				_ = unstructured.SetNestedField(result.Object, "added "+result.GetName(), "after")
				return result, nil
			},
			expectedClientAction: clienttesting.WatchActionImpl{
				ActionImpl: clienttesting.ActionImpl{
					Verb:     "watch",
					Resource: gvr("group", "version", "resources"),
				},
				WatchRestrictions: watchRestrictionsFromListOptions(metav1.ListOptions{}),
			},
			expectedClientActionCluster: "cluster1",
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
						watchTester.Add(resource("group/version", "Resource", "aThing")())
						watchTester.Add(resource("group/version", "Resource", "aThingMore")())
						watchTester.Modify(resource("group/version", "Resource", "aThing")())
						watchTester.Delete(resource("group/version", "Resource", "aThingMore")())
						watchTester.Error(watchedError)
					},
					[]watch.Event{
						{Type: watch.Added, Object: resource("group/version", "Resource", "aThingMore").field("after", "added aThingMore")()},
						{Type: watch.Deleted, Object: resource("group/version", "Resource", "aThingMore").field("after", "added aThingMore")()},
						{Type: watch.Error, Object: watchedError},
					})
			},
			expectedTransformerCalls: []transformerCall{
				{
					Moment:    "after",
					GVR:       gvr("group", "version", "resources"),
					Resource:  resource("group/version", "Resource", "aThing")(),
					LCluster:  "cluster1",
					EventType: "ADDED",
				},
				{
					Moment:    "after",
					GVR:       gvr("group", "version", "resources"),
					Resource:  resource("group/version", "Resource", "aThingMore")(),
					LCluster:  "cluster1",
					EventType: "ADDED",
				},
				{
					Moment:    "after",
					GVR:       gvr("group", "version", "resources"),
					Resource:  resource("group/version", "Resource", "aThing")(),
					LCluster:  "cluster1",
					EventType: "MODIFIED",
				},
				{
					Moment:    "after",
					GVR:       gvr("group", "version", "resources"),
					Resource:  resource("group/version", "Resource", "aThingMore")(),
					LCluster:  "cluster1",
					EventType: "DELETED",
				},
			},
		},
		{
			name:     "watch in cluster - failure in transformer",
			lcluster: "cluster1",
			gvr:      gvr("group", "version", "resources"),
			action: func(ctx context.Context, transformingClient kcpdynamic.ClusterInterface, lcluster logicalcluster.Name, gvr schema.GroupVersionResource) (result interface{}, err error) {
				return transformingClient.Resource(gvr).Cluster(lcluster).Watch(ctx, metav1.ListOptions{})
			},
			after: func(resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				if resource.GetName() == "aThing" {
					return nil, kerrors.NewForbidden(schema.GroupResource{Group: "group", Resource: "resources"}, "aThing", nil)
				}
				return nil, errors.New("raw error")
			},
			expectedClientAction: clienttesting.WatchActionImpl{
				ActionImpl: clienttesting.ActionImpl{
					Verb:     "watch",
					Resource: gvr("group", "version", "resources"),
				},
				WatchRestrictions: watchRestrictionsFromListOptions(metav1.ListOptions{}),
			},
			expectedClientActionCluster: "cluster1",
			checkResult: func(t *testing.T, watchTester *watch.FakeWatcher, result interface{}) {
				watcher, ok := result.(watch.Interface)
				if !ok {
					require.Fail(t, "result of Watch should be a watch.Interface")
				}

				checkWatchEvents(t,
					watcher,
					func() {
						watchTester.Add(resource("group/version", "Resource", "aThing")())
						watchTester.Add(resource("group/version", "Resource", "aThingMore")())
					},
					[]watch.Event{
						{Type: watch.Error, Object: &metav1.Status{
							Status:  "Failure",
							Message: `resources.group "aThing" is forbidden: <nil>`,
							Reason:  "Forbidden",
							Details: &metav1.StatusDetails{
								Name:  "aThing",
								Group: "group",
								Kind:  "resources",
							},
							Code: 403,
						}},
						{Type: watch.Error, Object: &metav1.Status{
							Status:  "Failure",
							Message: `Watch transformation failed`,
							Details: &metav1.StatusDetails{
								Name:  "aThingMore",
								Group: "group",
								Kind:  "Resource",
								Causes: []metav1.StatusCause{
									{
										Type:    metav1.CauseTypeUnexpectedServerResponse,
										Message: "raw error",
									},
								},
							},
							Code: 500,
						}},
					})
			},
			expectedTransformerCalls: []transformerCall{
				{
					Moment:    "after",
					GVR:       gvr("group", "version", "resources"),
					Resource:  resource("group/version", "Resource", "aThing")(),
					LCluster:  "cluster1",
					EventType: "ADDED",
				},
				{
					Moment:    "after",
					GVR:       gvr("group", "version", "resources"),
					Resource:  resource("group/version", "Resource", "aThingMore")(),
					LCluster:  "cluster1",
					EventType: "ADDED",
				},
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
			rt := &resourceTransformer{
				before: test.before,
				after:  test.after,
			}
			fakeWatcher := watch.NewFake()
			defer fakeWatcher.Stop()
			fakeClient.PrependWatchReactor("resources", clienttesting.DefaultWatchReactor(fakeWatcher, nil))

			transformingClient := WithResourceTransformer(clusterClient, rt)
			ctx := context.Background()

			result, err := test.action(ctx, transformingClient, logicalcluster.New(test.lcluster), test.gvr)

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
			actions := fakeClient.Actions()
			if len(actions) > 1 {
				require.Fail(t, "client action number should not be > 1")
			}
			var action clienttesting.Action
			if len(actions) == 1 {
				action = actions[0]
			}
			require.Empty(t, cmp.Diff(test.expectedClientAction, action), "client action is wrong")

			var lclusterRequestedInAction string
			if len(lclustersRequestedInActions) > 0 {
				lclusterRequestedInAction = lclustersRequestedInActions[0]
			}
			require.Equal(t, test.expectedClientActionCluster, lclusterRequestedInAction, "client lcluster is wrong")
			require.Empty(t, cmp.Diff(test.expectedTransformerCalls, rt.calls), "transformer calls are wrong")
		})
	}
}

func sortUnstructured(a *unstructured.Unstructured, b *unstructured.Unstructured) bool {
	return a.GetName() > b.GetName()
}

func listRestrictionsFromListOptions(options metav1.ListOptions) clienttesting.ListRestrictions {
	labelSelector, fieldSelector, _ := clienttesting.ExtractFromListOptions(options)
	return clienttesting.ListRestrictions{
		Labels: labelSelector,
		Fields: fieldSelector,
	}
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
