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

package resources

import (
	"context"
	"embed"
	"fmt"
	"time"

	extensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/klog/v2"

	configcrds "github.com/kcp-dev/kcp/config/crds"
)

// Bootstrap creates a list of CRDs and then the resources in a package's fs by
// continuously retrying the list. This is blocking, i.e. it only returns (with error)
// when the context is closed or with nil when the bootstrapping is successfully completed.
func Bootstrap(ctx context.Context, crdClient apiextensionsclient.Interface, dynamicClient dynamic.Interface, fs embed.FS, crds []metav1.GroupResource) error {
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(crdClient.Discovery()))

	if err := wait.PollImmediateInfiniteWithContext(ctx, time.Second, func(ctx context.Context) (bool, error) {
		if err := configcrds.Create(ctx, crdClient.ApiextensionsV1().CustomResourceDefinitions(), crds...); err != nil {
			klog.Errorf("failed to bootstrap CRDs: %v", err)
			return false, nil // keep retrying
		}
		return true, nil
	}); err != nil {
		return fmt.Errorf("failed to bootstrap CRDs: %w", err)
	}

	// bootstrap non-crd resources
	return wait.PollImmediateInfiniteWithContext(ctx, time.Second, func(ctx context.Context) (bool, error) {
		// +lint:noerrcheck
		if err := CreateResourcesFromFS(ctx, dynamicClient, mapper, fs); err != nil {
			klog.Infof("Failed to bootstrap resources, retrying: %v", err)
			return false, nil
		}
		return true, nil
	})
}

func CreateResourcesFromFS(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, fs embed.FS) error {
	files, err := fs.ReadDir(".")
	if err != nil {
		return err
	}

	var errs []error
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		if err := CreateResourceFromFS(ctx, client, mapper, f.Name(), fs); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.NewAggregate(errs)
}

// CreateResourceFromFS creates given resource file.
func CreateResourceFromFS(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, filename string, fs embed.FS) error {
	raw, err := fs.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("could not read %s: %w", filename, err)
	}

	if len(raw) == 0 {
		return nil // ignore empty files
	}

	obj, gvk, err := extensionsapiserver.Codecs.UniversalDeserializer().Decode(raw, nil, &unstructured.Unstructured{})
	if err != nil {
		return fmt.Errorf("could not decode raw %s: %w", filename, err)
	}
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("decoded %s into incorrect type, got %T, wanted %T", filename, obj, &unstructured.Unstructured{})
	}

	m, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("could not get REST mapping for %s: %w", filename, err)
	}

	if _, err := client.Resource(m.Resource).Namespace(u.GetNamespace()).Create(ctx, u, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			existing, err := client.Resource(m.Resource).Namespace(u.GetNamespace()).Get(ctx, u.GetName(), metav1.GetOptions{})
			if err != nil {
				return err
			}
			u.SetResourceVersion(existing.GetResourceVersion())
			if _, err = client.Resource(m.Resource).Namespace(u.GetNamespace()).Update(ctx, u, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("could not update %s: %w", filename, err)
			} else {
				klog.Infof("Updated %s", filename)
				return nil
			}
		}
		return err
	}

	klog.Infof("Bootstrapped %s", filename)

	return nil
}
