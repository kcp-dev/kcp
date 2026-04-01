/*
Copyright 2021 The kcp Authors.

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

package helpers

import (
	"context"
	"embed"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/klog/v2"

	tenancyhelper "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1/helper"

	"github.com/kcp-dev/kcp/pkg/errgroup"
	"github.com/kcp-dev/kcp/pkg/logging"
)

// Bootstrap creates resources in a package's fs by continuously
// retrying the list. This is blocking, i.e. it only returns (with
// error) when the context is closed or with nil when the bootstrapping
// is successfully completed.
func Bootstrap(ctx context.Context, discoveryClient discovery.DiscoveryInterface, dynamicClient dynamic.Interface, batteriesIncluded sets.Set[string], efs embed.FS, opts ...Option) error {
	cache := memory.NewMemCacheClient(discoveryClient)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(cache)

	// bootstrap non-crd resources
	transformers := make([]TransformFileFunc, 0, len(opts))
	for _, opt := range opts {
		transformers = append(transformers, opt.TransformFile)
	}

	resources, err := ReadResourcesFromFS(
		ctx,
		efs,
		transformers,
		batteriesIncluded,
	)
	if err != nil {
		return err
	}

	for _, group := range GroupObjectsByDefaultHierarchy(resources) {
		if err := bootstrapGroup(ctx, dynamicClient, mapper, group, cache.Invalidate); err != nil {
			return err
		}
	}
	return nil
}

func bootstrapGroup(ctx context.Context, dynamicClient dynamic.Interface, mapper meta.RESTMapper, resources []*unstructured.Unstructured, reset func()) error {
	return wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		if err := CreateResources(ctx, dynamicClient, mapper, resources); err != nil {
			klog.FromContext(ctx).WithValues("err", err).Info("failed to bootstrap resources, retrying")
			// invalidate cache if resources not found
			// xref: https://github.com/kcp-dev/kcp/issues/655
			// After adding ordering to the installation this
			// technically isn't needed anymore - however other projects
			// are also using the Bootstrap function and their resource
			// dependencies might not be reflected in the weights, hence
			// the cache invalidation is carried forward.
			reset()
			return false, nil
		}
		return true, nil
	})
}

// CreateResourcesFromFS creates all resources from a filesystem.
func CreateResourcesFromFS(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, batteriesIncluded sets.Set[string], efs embed.FS, transformers ...TransformFileFunc) error {
	resources, err := ReadResourcesFromFS(
		ctx,
		efs,
		transformers,
		batteriesIncluded,
	)
	if err != nil {
		return err
	}

	groups := GroupObjectsByDefaultHierarchy(resources)
	if err := CreateGroupedResources(ctx, client, mapper, groups); err != nil {
		return fmt.Errorf("could not create resources from groups: %w", err)
	}
	return nil
}

// CreateResourceFromFS creates given resource file.
func CreateResourceFromFS(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, batteriesIncluded sets.Set[string], filename string, efs embed.FS, transformers ...TransformFileFunc) error {
	resources, err := ReadResourceFromFS(
		ctx,
		efs,
		filename,
		transformers,
		batteriesIncluded,
	)
	if err != nil {
		return err
	}

	groups := GroupObjectsByDefaultHierarchy(resources)
	if err := CreateGroupedResources(ctx, client, mapper, groups); err != nil {
		return fmt.Errorf("could not create resources from groups: %w", err)
	}
	return nil
}

// CreateGroupedResources sequentially runs CreateResources for each group of resources.
func CreateGroupedResources(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, groups [][]*unstructured.Unstructured) error {
	for i, resources := range groups {
		if err := CreateResources(ctx, client, mapper, resources); err != nil {
			return fmt.Errorf("failed to create resources from group %d: %w", i, err)
		}
	}
	return nil
}

// CreateResources creates all resources from a group of resources.
func CreateResources(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, resources []*unstructured.Unstructured) error {
	g := errgroup.WithContext(ctx)

	for _, obj := range resources {
		g.Go(func(ctx context.Context) error {
			return createResource(ctx, client, mapper, obj)
		})
	}

	return g.Wait()
}

const annotationCreateOnlyKey = "bootstrap.kcp.io/create-only"
const annotationBattery = "bootstrap.kcp.io/battery"

func createResource(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, u *unstructured.Unstructured) error {
	logger := klog.FromContext(ctx)

	gvk := u.GetObjectKind().GroupVersionKind()

	m, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("could not get REST mapping for %s: %w", gvk, err)
	}

	// Clear resourceVersion before CREATE - it may have been set by a previous
	// failed attempt that got AlreadyExists and then retried.
	u.SetResourceVersion("")

	upserted, err := client.Resource(m.Resource).Namespace(u.GetNamespace()).Create(ctx, u, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			existing, err := client.Resource(m.Resource).Namespace(u.GetNamespace()).Get(ctx, u.GetName(), metav1.GetOptions{})
			if err != nil {
				return err
			}
			logger := logging.WithObject(logger, existing)

			if _, exists := existing.GetAnnotations()[annotationCreateOnlyKey]; exists {
				logger.Info("skipping update of object because it has the create-only annotation")
				return nil
			}

			u.SetResourceVersion(existing.GetResourceVersion())
			if _, err = client.Resource(m.Resource).Namespace(u.GetNamespace()).Update(ctx, u, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("could not update %s %s: %w", gvk.Kind, tenancyhelper.QualifiedObjectName(existing), err)
			}
			logger.Info("updated object")
			return nil
		}
		return err
	}

	logging.WithObject(logger, upserted).Info("upserted object")

	return nil
}
