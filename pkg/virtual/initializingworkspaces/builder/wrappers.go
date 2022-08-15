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

package builder

import (
	"context"
	"fmt"

	"github.com/kcp-dev/logicalcluster/v2"

	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/projection"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/forwardingregistry"
)

type clusterWorkspaceProjectionClient struct {
	dynamic.ClusterInterface
}

func (c clusterWorkspaceProjectionClient) Cluster(name logicalcluster.Name) dynamic.Interface {
	return workspaceProjectionClient{c.ClusterInterface.Cluster(name)}
}

type workspaceProjectionClient struct {
	dynamic.Interface
}

func (c workspaceProjectionClient) Resource(resource schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	if resource.GroupResource() == tenancyv1alpha1.Resource("clusterworkspaces") {
		resource = tenancyv1beta1.SchemeGroupVersion.WithResource("workspaces")
	}
	return c.Interface.Resource(resource)
}

func WithWorkspaceProjection(delegateWrapper forwardingregistry.StorageWrapper) forwardingregistry.StorageWrapper {
	return func(resource schema.GroupResource, storage *forwardingregistry.StoreFuncs) *forwardingregistry.StoreFuncs {
		storage = delegateWrapper(resource, storage)

		delegateGetter := storage.GetterFunc
		storage.GetterFunc = func(ctx context.Context, name string, opts *metav1.GetOptions) (runtime.Object, error) {
			obj, err := delegateGetter(ctx, name, opts)
			if err != nil {
				return obj, err
			}

			u, ok := obj.(*unstructured.Unstructured)
			if !ok {
				return nil, fmt.Errorf("unexpected type %T", obj)
			}

			cws := &tenancyv1alpha1.ClusterWorkspace{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, cws); err != nil {
				return nil, fmt.Errorf("failed to convert unstructured to ClusterWorkspace: %w", err)
			}

			ws := &tenancyv1beta1.Workspace{}
			projection.ProjectClusterWorkspaceToWorkspace(cws, ws)

			// write back
			raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(ws)
			if err != nil {
				return nil, err
			}
			u.Object = raw

			return u, nil
		}

		delegateLister := storage.ListerFunc
		storage.ListerFunc = func(ctx context.Context, opts *metainternal.ListOptions) (runtime.Object, error) {
			obj, err := delegateLister(ctx, opts)
			if err != nil {
				return obj, err
			}

			ul, ok := obj.(*unstructured.UnstructuredList)
			if !ok {
				return nil, fmt.Errorf("unexpected type %T", obj)
			}

			for i := range ul.Items {
				u := &ul.Items[i]

				cws := &tenancyv1alpha1.ClusterWorkspace{}
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, cws); err != nil {
					return nil, fmt.Errorf("failed to convert unstructured to ClusterWorkspace: %w", err)
				}

				ws := &tenancyv1beta1.Workspace{}
				projection.ProjectClusterWorkspaceToWorkspace(cws, ws)

				// write back
				raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(ws)
				if err != nil {
					return nil, err
				}
				u.Object = raw
			}

			return ul, nil
		}

		delegateWatcher := storage.WatcherFunc
		storage.WatcherFunc = func(ctx context.Context, opts *metainternal.ListOptions) (watch.Interface, error) {
			w, err := delegateWatcher(ctx, opts)
			if err != nil {
				return nil, err
			}
			return workspaceWatcher{delegate: w, ch: make(chan watch.Event)}, nil
		}

		return storage
	}
}

type workspaceWatcher struct {
	delegate watch.Interface
	ch       chan watch.Event
}

func (w workspaceWatcher) ResultChan() <-chan watch.Event {
	ch := w.delegate.ResultChan()

	go func() {
		for ev := range ch {
			if ev.Object == nil {
				w.ch <- ev
				continue
			}
			if u, ok := ev.Object.(*unstructured.Unstructured); ok {
				cws := &tenancyv1alpha1.ClusterWorkspace{}
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, cws); err != nil {
					continue
				}

				ws := &tenancyv1beta1.Workspace{}
				projection.ProjectClusterWorkspaceToWorkspace(cws, ws)

				// write back
				raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(ws)
				if err != nil {
					continue
				}
				u.Object = raw
			}

			w.ch <- ev
		}
	}()

	return w.ch
}

func (w workspaceWatcher) Stop() {
	defer close(w.ch)
	w.delegate.Stop()
}
