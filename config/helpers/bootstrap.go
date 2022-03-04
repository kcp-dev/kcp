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

package helpers

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"time"

	extensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apimachineryerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/klog/v2"

	configcrds "github.com/kcp-dev/kcp/config/crds"
)

// TransformFileFunc transforms a resource file before being applied to the cluster.
type TransformFileFunc func(bs []byte) ([]byte, error)

// Option allows to customize the bootstrap process.
type Option struct {
	// TransformFileFunc is a function that transforms a resource file before being applied to the cluster.
	TransformFile TransformFileFunc
}

// ReplaceOption allows to customize the bootstrap process.
func ReplaceOption(pairs ...string) Option {
	return Option{
		TransformFile: func(bs []byte) ([]byte, error) {
			if len(pairs)%2 != 0 {
				return nil, fmt.Errorf("odd number of arguments: %v", pairs)
			}
			for i := 0; i < len(pairs); i += 2 {
				bs = bytes.ReplaceAll(bs, []byte(pairs[i]), []byte(pairs[i+1]))
			}
			return bs, nil
		},
	}
}

// Bootstrap creates a list of CRDs and then the resources in a package's fs by
// continuously retrying the list. This is blocking, i.e. it only returns (with error)
// when the context is closed or with nil when the bootstrapping is successfully completed.
func Bootstrap(ctx context.Context, crdClient apiextensionsclient.Interface, dynamicClient dynamic.Interface, fs embed.FS, crds []metav1.GroupResource, opts ...Option) error {
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
	var transformers []TransformFileFunc
	for _, opt := range opts {
		transformers = append(transformers, opt.TransformFile)
	}
	return wait.PollImmediateInfiniteWithContext(ctx, time.Second, func(ctx context.Context) (bool, error) {
		if err := CreateResourcesFromFS(ctx, dynamicClient, mapper, fs, transformers...); err != nil {
			klog.Infof("Failed to bootstrap resources, retrying: %v", err)
			return false, nil
		}
		return true, nil
	})
}

// CreateResourcesFromFS creates all resources from a filesystem.
func CreateResourcesFromFS(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, fs embed.FS, transformers ...TransformFileFunc) error {
	files, err := fs.ReadDir(".")
	if err != nil {
		return err
	}

	var errs []error
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		if err := CreateResourceFromFS(ctx, client, mapper, f.Name(), fs, transformers...); err != nil {
			errs = append(errs, err)
		}
	}
	return apimachineryerrors.NewAggregate(errs)
}

// CreateResourceFromFS creates given resource file.
func CreateResourceFromFS(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, filename string, fs embed.FS, transformers ...TransformFileFunc) error {
	raw, err := fs.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("could not read %s: %w", filename, err)
	}

	if len(raw) == 0 {
		return nil // ignore empty files
	}

	d := kubeyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(raw)))
	var errs []error
	for i := 1; ; i++ {
		doc, err := d.Read()
		if errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return err
		}
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}

		for _, transformer := range transformers {
			doc, err = transformer(doc)
			if err != nil {
				return err
			}
		}

		if err := createResourceFromFS(ctx, client, mapper, doc); err != nil {
			errs = append(errs, fmt.Errorf("failed to create resource %s doc %d: %w", filename, i, err))
		}
	}
	return apimachineryerrors.NewAggregate(errs)
}

func createResourceFromFS(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, raw []byte) error {
	obj, gvk, err := extensionsapiserver.Codecs.UniversalDeserializer().Decode(raw, nil, &unstructured.Unstructured{})
	if err != nil {
		return fmt.Errorf("could not decode raw: %w", err)
	}
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("decoded into incorrect type, got %T, wanted %T", obj, &unstructured.Unstructured{})
	}

	m, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("could not get REST mapping for %s: %w", gvk, err)
	}

	upserted, err := client.Resource(m.Resource).Namespace(u.GetNamespace()).Create(ctx, u, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			existing, err := client.Resource(m.Resource).Namespace(u.GetNamespace()).Get(ctx, u.GetName(), metav1.GetOptions{})
			if err != nil {
				return err
			}
			u.SetResourceVersion(existing.GetResourceVersion())
			if upserted, err = client.Resource(m.Resource).Namespace(u.GetNamespace()).Update(ctx, u, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("could not update %s %s/%s: %w", gvk.Kind, u.GetNamespace(), u.GetName(), err)
			} else {
				klog.Infof("Updated %s %s|%s", gvk, upserted.GetClusterName(), upserted.GetName())
				return nil
			}
		}
		return err
	}

	klog.Infof("Bootstrapped %s %s|%s", gvk.Kind, upserted.GetClusterName(), upserted.GetName())

	return nil
}
