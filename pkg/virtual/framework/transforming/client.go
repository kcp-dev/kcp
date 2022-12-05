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

	"github.com/go-logr/logr"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
	clientdynamic "github.com/kcp-dev/kcp/pkg/virtual/framework/client/dynamic"
)

type transformingDynamicClusterClient struct {
	transformer ResourceTransformer
	delegate    kcpdynamic.ClusterInterface
}

func (c *transformingDynamicClusterClient) Cluster(name logicalcluster.Name) dynamic.Interface {
	return &transformingDynamicClient{
		transformer: c.transformer,
		delegate:    c.delegate.Cluster(name),
	}
}

func (c *transformingDynamicClusterClient) Resource(resource schema.GroupVersionResource) kcpdynamic.ResourceClusterInterface {
	delegate := c.delegate.Resource(resource)
	return &transformingResourceClusterClient{
		transformingListerWatcherClient: transformingListerWatcherClient{
			delegate:    delegate,
			transformer: c.transformer,
			resourceClient: func(resource logicalcluster.Object) dynamic.ResourceInterface {
				return delegate.Cluster(logicalcluster.From(resource))
			},
			resource: resource,
		},
		delegate: delegate,
	}
}

type ResourceInterfaceWithResults interface {
	dynamic.ResourceInterface
	clientdynamic.DeleterWithResults
}

type transformingResourceClusterClient struct {
	transformingListerWatcherClient
	delegate kcpdynamic.ResourceClusterInterface
}

func (trc *transformingResourceClusterClient) Cluster(workspace logicalcluster.Name) dynamic.NamespaceableResourceInterface {
	delegate := trc.delegate.Cluster(workspace)
	return &transformingNamespaceableResourceClient{
		transformer:                    trc.transformer,
		namespaceableResourceInterface: delegate,
		ResourceInterfaceWithResults: &transformingResourceClient{
			transformingListerWatcherClient: transformingListerWatcherClient{
				transformer:    trc.transformer,
				delegate:       delegate,
				resourceClient: func(logicalcluster.Object) dynamic.ResourceInterface { return delegate },
				resource:       trc.resource,
			},
			delegate: delegate,
		},
		resource: trc.resource,
	}
}

type transformingDynamicClient struct {
	transformer ResourceTransformer
	delegate    dynamic.Interface
}

func (c *transformingDynamicClient) Resource(resource schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	delegate := c.delegate.Resource(resource)
	return &transformingNamespaceableResourceClient{
		transformer:                    c.transformer,
		namespaceableResourceInterface: c.delegate.Resource(resource),
		ResourceInterfaceWithResults: &transformingResourceClient{
			transformingListerWatcherClient: transformingListerWatcherClient{
				transformer:    c.transformer,
				delegate:       delegate,
				resourceClient: func(logicalcluster.Object) dynamic.ResourceInterface { return delegate },
				resource:       resource,
			},
			delegate: delegate,
		},
		resource: resource,
	}
}

type transformingNamespaceableResourceClient struct {
	transformer                    ResourceTransformer
	namespaceableResourceInterface dynamic.NamespaceableResourceInterface
	ResourceInterfaceWithResults
	resource schema.GroupVersionResource
}

func (c *transformingNamespaceableResourceClient) Namespace(namespace string) dynamic.ResourceInterface {
	delegate := c.namespaceableResourceInterface.Namespace(namespace)
	return &transformingResourceClient{
		transformingListerWatcherClient: transformingListerWatcherClient{
			transformer:    c.transformer,
			delegate:       delegate,
			resourceClient: func(logicalcluster.Object) dynamic.ResourceInterface { return delegate },
			resource:       c.resource,
		},
		delegate: delegate,
	}
}

type listerWatcher interface {
	List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error)
	Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
}

type transformingListerWatcherClient struct {
	transformer    ResourceTransformer
	delegate       listerWatcher
	resourceClient func(resource logicalcluster.Object) dynamic.ResourceInterface
	resource       schema.GroupVersionResource
}

// List implements dynamic.ResourceInterface.
// It delegates the List call to the underlying kubernetes client,
// and transforms back every item of the List call result by calling the transformer AfterRead method.
func (tc *transformingListerWatcherClient) List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	var err error
	result, err := tc.delegate.List(ctx, opts)
	if err != nil && result == nil {
		return nil, err
	}

	afterLogger := getLogger(ctx).WithValues("action", "list").WithValues("moment", after).WithValues("labelselector", opts.LabelSelector)
	transformedResult := result.DeepCopy()
	transformedResult.Items = []unstructured.Unstructured{}
	for _, item := range result.Items {
		item := item
		itemAfterLogger := logging.WithObject(afterLogger, &item)
		itemAfterLogger.Info(startingMessage)
		if transformed, err := tc.transformer.AfterRead(tc.resourceClient(&item), ctx, tc.resource, &item, nil); err != nil {
			if kerrors.IsNotFound(err) {
				itemAfterLogger.Info("transformation did return a NotFound error: let's skip the item")
				continue
			}
			itemAfterLogger.Error(err, errorMessage)
			return nil, err
		} else {
			transformedResult.Items = append(transformedResult.Items, *transformed)
		}
	}
	return transformedResult, err
}

// Watch implements dynamic.ResourceInterface.
// It delegates the Watch call to the underlying kubernetes client,
// and transforms every event delivered by the kubernetes client watcher, by calling the transformer AfterRead method
// wrapped inside an EventTransformer.
func (tc *transformingListerWatcherClient) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	var err error
	result, err := tc.delegate.Watch(ctx, opts)
	if err != nil {
		return result, err
	}

	afterLogger := getLogger(ctx).WithValues("action", "watch").WithValues("moment", after).WithValues("labelselector", opts.LabelSelector)

	transformingWatcher := NewTransformingWatcher(result, func(event watch.Event) *watch.Event {
		transformed := event
		eventType := event.Type
		itemAfterLogger := afterLogger.WithValues("event", eventType)
		if eventType == watch.Bookmark || eventType == watch.Error {
			itemAfterLogger.Info("don't transform bookmark and error events")
			return &transformed
		}
		resource, ok := event.Object.(*unstructured.Unstructured)
		if !ok {
			errorMessage := "watch expected a resource of type *unstructured.Unstructured"
			itemAfterLogger.Error(errors.New(errorMessage), errorMessage)

			transformed.Type = watch.Error
			transformed.Object = &metav1.Status{
				Status:  "Failure",
				Reason:  metav1.StatusReasonUnknown,
				Message: errorMessage,
				Code:    500,
			}
			return &transformed
		}
		if resource != nil {
			itemAfterLogger = logging.WithObject(itemAfterLogger, resource)
		}
		itemAfterLogger.Info(startingMessage)
		if transformedResource, err := tc.transformer.AfterRead(tc.resourceClient(resource), ctx, tc.resource, resource, &eventType); err != nil {
			if kerrors.IsNotFound(err) {
				itemAfterLogger.Info("transformation did return a NotFound error: let's skip the item")
				return nil
			}
			itemAfterLogger.Error(err, errorMessage)
			transformed.Type = watch.Error
			statusError := &kerrors.StatusError{}
			if errors.As(err, &statusError) {
				transformed.Object = statusError.ErrStatus.DeepCopy()
			} else {
				transformed.Object = &metav1.Status{
					Status:  "Failure",
					Reason:  metav1.StatusReasonUnknown,
					Message: "Watch transformation failed",
					Code:    500,
					Details: &metav1.StatusDetails{
						Name:  resource.GetName(),
						Group: resource.GroupVersionKind().Group,
						Kind:  resource.GroupVersionKind().Kind,
						Causes: []metav1.StatusCause{
							{
								Type:    metav1.CauseTypeUnexpectedServerResponse,
								Message: err.Error(),
							},
						},
					},
				}
			}
		} else {
			transformed.Object = transformedResource
		}
		return &transformed
	})
	return transformingWatcher, nil
}

type transformingResourceClient struct {
	transformingListerWatcherClient
	delegate dynamic.ResourceInterface
}

func getLogger(ctx context.Context) logr.Logger {
	return klog.FromContext(ctx).WithName("transforming-client").V(7)
}

const (
	before          = "before"
	after           = "after"
	errorMessage    = "error during transformation"
	startingMessage = "starting transformation"
)

// Create implements dynamic.ResourceInterface.
// It transforms the input resource by calling the transformer BeforeWrite method
// before delegating the Create call to the underlying kubernetes client,
// and transforms back the result of the Create call by calling the transformer AfterRead method.
func (tc *transformingResourceClient) Create(ctx context.Context, obj *unstructured.Unstructured, options metav1.CreateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	var err error
	logger := getLogger(ctx).WithValues("subresources", subresources).WithValues("action", "create")
	if obj != nil {
		logger = logging.WithObject(logger, obj)
	}
	beforeLogger := logger.WithValues("moment", before)
	beforeLogger.Info(startingMessage)
	obj, err = tc.transformer.BeforeWrite(tc.delegate, ctx, tc.resource, obj, subresources...)
	if err != nil {
		beforeLogger.Error(err, errorMessage)
		return nil, err
	}
	result, err := tc.delegate.Create(ctx, obj, options, subresources...)
	if err != nil {
		return result, err
	}
	afterLogger := logger.WithValues("moment", after)
	afterLogger.Info(startingMessage)
	result, err = tc.transformer.AfterRead(tc.delegate, ctx, tc.resource, result, nil, subresources...)
	if err != nil {
		afterLogger.Error(err, errorMessage)
		return result, err
	}
	return result, err
}

// Update implements dynamic.ResourceInterface.
// It transforms the input resource by calling the transformer BeforeWrite method
// before delegating the Update call to the underlying kubernetes client,
// and transforms back the result of the Update call by calling the transformer AfterRead method.
func (tc *transformingResourceClient) Update(ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	var err error
	logger := getLogger(ctx).WithValues("subresources", subresources).WithValues("action", "update")
	if obj != nil {
		logger = logging.WithObject(logger, obj)
	}
	beforeLogger := logger.WithValues("moment", before)
	beforeLogger.Info(startingMessage)
	obj, err = tc.transformer.BeforeWrite(tc.delegate, ctx, tc.resource, obj, subresources...)
	if err != nil {
		beforeLogger.Error(err, errorMessage)
		return nil, err
	}
	result, err := tc.delegate.Update(ctx, obj, options, subresources...)
	if err != nil {
		return result, err
	}
	afterLogger := logger.WithValues("moment", after)
	afterLogger.Info(startingMessage)
	result, err = tc.transformer.AfterRead(tc.delegate, ctx, tc.resource, result, nil, subresources...)
	if err != nil {
		afterLogger.Error(err, errorMessage)
		return result, err
	}
	return result, err
}

// Get implements dynamic.ResourceInterface.
// It delegates the Get call to the underlying kubernetes client,
// and transforms back the result of the Get call by calling the transformer AfterRead method.
func (tc *transformingResourceClient) Get(ctx context.Context, name string, options metav1.GetOptions, subresources ...string) (*unstructured.Unstructured, error) {
	var err error
	result, err := tc.delegate.Get(ctx, name, options, subresources...)
	if err != nil {
		return result, err
	}
	afterLogger := getLogger(ctx).WithValues("name", name, "subresources", subresources).WithValues("action", "get").WithValues("moment", after)
	afterLogger.Info(startingMessage)
	result, err = tc.transformer.AfterRead(tc.delegate, ctx, tc.resource, result, nil, subresources...)
	if err != nil {
		afterLogger.Error(err, errorMessage)
		return result, err
	}
	return result, err
}

func (tc *transformingResourceClient) UpdateStatus(ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return tc.Update(ctx, obj, options, "status")
}

func (tc *transformingResourceClient) Delete(ctx context.Context, name string, options metav1.DeleteOptions, subresources ...string) error {
	return tc.delegate.Delete(ctx, name, options, subresources...)
}

func (tc *transformingResourceClient) DeleteCollection(ctx context.Context, options metav1.DeleteOptions, listOptions metav1.ListOptions) error {
	return tc.delegate.DeleteCollection(ctx, options, listOptions)
}

func (tc *transformingResourceClient) DeleteWithResult(ctx context.Context, name string, options metav1.DeleteOptions, subresources ...string) (*unstructured.Unstructured, int, error) {
	delegateDeleter, err := clientdynamic.NewDeleterWithResults(tc.delegate)
	if err != nil {
		return nil, 0, err
	}
	result, statusCode, err := delegateDeleter.DeleteWithResult(ctx, name, options, subresources...)
	if err != nil {
		return result, statusCode, err
	}
	afterLogger := getLogger(ctx).WithValues("name", name, "subresources", subresources).WithValues("action", "delete").WithValues("moment", after)
	afterLogger.Info(startingMessage)
	result, err = tc.transformer.AfterRead(tc.delegate, ctx, tc.resource, result, nil, subresources...)
	if err != nil {
		afterLogger.Error(err, errorMessage)
		return result, statusCode, err
	}
	return result, statusCode, err
}

func (tc *transformingResourceClient) DeleteCollectionWithResult(ctx context.Context, options metav1.DeleteOptions, listOptions metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	delegateDeleter, err := clientdynamic.NewDeleterWithResults(tc.delegate)
	if err != nil {
		return nil, err
	}
	result, err := delegateDeleter.DeleteCollectionWithResult(ctx, options, listOptions)
	if err != nil && result == nil {
		return nil, err
	}

	afterLogger := getLogger(ctx).WithValues("action", "deletecollection").WithValues("moment", after).WithValues("labelselector", listOptions.LabelSelector)

	transformedResult := result.DeepCopy()
	transformedResult.Items = []unstructured.Unstructured{}
	for _, item := range result.Items {
		item := item
		itemAfterLogger := logging.WithObject(afterLogger, &item)
		itemAfterLogger.Info(startingMessage)
		if transformed, err := tc.transformer.AfterRead(tc.resourceClient(&item), ctx, tc.resource, &item, nil); err != nil {
			if kerrors.IsNotFound(err) {
				itemAfterLogger.Info("transformation did return a NotFound error: let's skip the item")
				continue
			}
			itemAfterLogger.Error(err, errorMessage)
			return nil, err
		} else {
			transformedResult.Items = append(transformedResult.Items, *transformed)
		}
	}
	return transformedResult, err
}

func (tc *transformingResourceClient) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, options metav1.PatchOptions, subresources ...string) (*unstructured.Unstructured, error) {
	return nil, errors.New("not implemented")
}
