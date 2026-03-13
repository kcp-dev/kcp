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
	"bytes"
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"text/template"
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

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	tenancyhelper "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1/helper"
	kcpclient "github.com/kcp-dev/sdk/client/clientset/versioned"

	"github.com/kcp-dev/kcp/pkg/errgroup"
	"github.com/kcp-dev/kcp/pkg/logging"
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

// Bootstrap creates resources in a package's fs by
// continuously retrying the list. This is blocking, i.e. it only returns (with error)
// when the context is closed or with nil when the bootstrapping is successfully completed.
func Bootstrap(ctx context.Context, discoveryClient discovery.DiscoveryInterface, dynamicClient dynamic.Interface, batteriesIncluded sets.Set[string], embedFS embed.FS, opts ...Option) error {
	cache := memory.NewMemCacheClient(discoveryClient)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(cache)

	// bootstrap non-crd resources
	transformers := make([]TransformFileFunc, 0, len(opts))
	for _, opt := range opts {
		transformers = append(transformers, opt.TransformFile)
	}

	resources, err := ReadResourcesFromFS(ctx, embedFS, nil, batteriesIncluded, transformers...)
	if err != nil {
		return fmt.Errorf("could not read resources from FS: %w", err)
	}

	chunks := ChunkObjectsByHierarchy(resources)
	// invalidate cache if resources not found
	// xref: https://github.com/kcp-dev/kcp/issues/655
	if err := CreateResourcesFromChunks(ctx, dynamicClient, mapper, chunks, cache.Invalidate); err != nil {
		return fmt.Errorf("could not create resources from chunks: %w", err)
	}
	return nil
}

// CreateResourcesFromFS creates all resources from a filesystem.
func CreateResourcesFromFS(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, batteriesIncluded sets.Set[string], embedFS embed.FS, transformers ...TransformFileFunc) error {
	resources, err := ReadResourcesFromFS(ctx, embedFS, nil, batteriesIncluded, transformers...)
	if err != nil {
		return fmt.Errorf("could not read resources from FS: %w", err)
	}

	chunks := ChunkObjectsByHierarchy(resources)
	if err := CreateResourcesFromChunks(ctx, client, mapper, chunks, nil); err != nil {
		return fmt.Errorf("could not create resources from chunks: %w", err)
	}

	return nil
}

func cleanPath(s string) string {
	s = filepath.Clean(s)
	s = strings.TrimPrefix(s, "/")
	return s
}

// CreateResourceFromFS creates given resource file.
// filename should be the absolute path within the filesystem. The leading slash is optional.
func CreateResourceFromFS(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, batteriesIncluded sets.Set[string], filename string, embedFS embed.FS, transformers ...TransformFileFunc) error {
	cleanFilename := cleanPath(filename)

	resources, err := ReadResourcesFromFS(ctx, embedFS, func(p string, d fs.DirEntry) (bool, error) {
		return cleanPath(p) == cleanFilename, nil
	}, batteriesIncluded, transformers...)
	if err != nil {
		return fmt.Errorf("could not read resources from FS: %w", err)
	}

	chunks := ChunkObjectsByHierarchy(resources)
	if err := CreateResourcesFromChunks(ctx, client, mapper, chunks, nil); err != nil {
		return fmt.Errorf("could not create resources from chunks: %w", err)
	}

	return nil
}

// DirEntryFilterFunc is used to filter which resources from a filesystem to read.
type DirEntryFilterFunc func(absPath string, dirEntry fs.DirEntry) (bool, error)

// ReadResourcesFromFS reads all resources from a filesystem and returns
// them as unstructured.Unstructured objects.
// If the filter function is not nil if is called for each file. If the
// filter function returns true the file is parsed, otherwise it is ignored.
func ReadResourcesFromFS(
	ctx context.Context,
	embedFS embed.FS,
	filter DirEntryFilterFunc,
	batteriesIncluded sets.Set[string],
	transformers ...TransformFileFunc,
) ([]*unstructured.Unstructured, error) {
	ret := []*unstructured.Unstructured{}

	// build the input for templating
	type Input struct {
		Batteries map[string]bool
	}
	input := Input{
		Batteries: map[string]bool{},
	}
	for _, b := range sets.List[string](batteriesIncluded) {
		input.Batteries[b] = true
	}

	if err := fs.WalkDir(embedFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		if filter != nil {
			parse, err := filter(path, d)
			if err != nil {
				return fmt.Errorf("could not filter %s: %w", path, err)
			}
			if !parse {
				return nil
			}
		}

		raw, err := embedFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("could not read %s: %w", path, err)
		}
		for _, transformer := range transformers {
			if raw, err = transformer(raw); err != nil {
				return fmt.Errorf("could not transform %s: %w", path, err)
			}
		}

		// some yaml docs are templates
		tmpl, err := template.New("manifest").Parse(string(raw))
		if err != nil {
			return fmt.Errorf("failed to parse manifest: %w", err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, input); err != nil {
			return fmt.Errorf("failed to execute manifest: %w", err)
		}

		resources, err := ParseYAML(buf.Bytes())
		if err != nil {
			return fmt.Errorf("could not parse %s: %w", path, err)
		}

		for _, resource := range resources {
			v, found := resource.GetAnnotations()[annotationBattery]
			if !found {
				// resource is not relevant to batteries-included, just
				// add it to be installed
				ret = append(ret, resource)
				continue
			}

			partOf := strings.Split(v, ",")
			included := false
			for _, p := range partOf {
				if batteriesIncluded.Has(strings.TrimSpace(p)) {
					included = true
					break
				}
			}
			if !included {
				continue
			}
			ret = append(ret, resource)
		}

		return nil
	}); err != nil {
		return nil, fmt.Errorf("could not walk embed FS: %w", err)
	}

	return ret, nil
}

// CreateResourcesFromChunks sequentially runs CreateResourcesFromChunk
// for each chunk of resources.
func CreateResourcesFromChunks(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, chunks [][]*unstructured.Unstructured, reset func()) error {
	for i, chunk := range chunks {
		if err := CreateResourcesFromChunk(ctx, client, mapper, chunk, reset); err != nil {
			return fmt.Errorf("failed to create resources from chunk %d: %w", i, err)
		}
	}
	return nil
}

// CreateResourcesFromChunk creates all resources from a chunk of resources.
// If reset is not nil it is called after each failed attempt, allowing to reset e.g. a cache.
func CreateResourcesFromChunk(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, chunk []*unstructured.Unstructured, reset func()) error {
	g := errgroup.WithContext(ctx)

	for _, obj := range chunk {
		g.Go(func(ctx context.Context) error {
			return retryCreateResource(ctx, client, mapper, obj, reset)
		})
	}

	return g.Wait()
}

func retryCreateResource(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, obj *unstructured.Unstructured, reset func()) error {
	return wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (done bool, err error) {
		if err := createResource(ctx, client, mapper, obj); err != nil {
			klog.FromContext(ctx).WithValues("err", err).Info("failed to bootstrap resources, retrying")
			if reset != nil {
				reset()
			}
			return false, nil
		}
		return true, nil
	})
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
			} else {
				logger.Info("updated object")
				return nil
			}
		}
		return err
	}

	logging.WithObject(logger, upserted).Info("upserted object")

	return nil
}

func BindRootAPIs(ctx context.Context, kcpClient kcpclient.Interface, exportNames ...string) error {
	g := errgroup.WithContext(ctx)
	for _, exportName := range exportNames {
		g.Go(func(ctx context.Context) error {
			if err := BindRootAPI(ctx, kcpClient, exportName); err != nil {
				return fmt.Errorf("failed to bind root API %s: %w", exportName, err)
			}
			return nil
		})
	}
	return g.Wait()
}

func BindRootAPI(ctx context.Context, kcpClient kcpclient.Interface, exportName string) error {
	logger := klog.FromContext(ctx)

	binding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: exportName,
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: core.RootCluster.Path().String(),
					Name: exportName,
				},
			},
		},
	}

	created, err := kcpClient.ApisV1alpha2().APIBindings().Create(ctx, binding, metav1.CreateOptions{})
	if err == nil {
		logger := logging.WithObject(logger, created)
		logger.V(2).Info("Created API binding")
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return err
	}

	return wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		existing, err := kcpClient.ApisV1alpha2().APIBindings().Get(ctx, exportName, metav1.GetOptions{})
		if err != nil {
			logger.Error(err, "error getting APIBinding", "name", exportName)
			// Always keep trying. Don't ever return an error out of this function.
			return false, nil
		}

		logger := logging.WithObject(logger, existing)
		logger.V(2).Info("Updating API binding")

		existing.Spec = binding.Spec

		_, err = kcpClient.ApisV1alpha2().APIBindings().Update(ctx, existing, metav1.UpdateOptions{})
		if err == nil {
			return true, nil
		}
		if apierrors.IsConflict(err) {
			logger.V(2).Info("API binding update conflict, retrying")
			return false, nil
		}

		logger.Error(err, "error updating APIBinding")
		// Always keep trying. Don't ever return an error out of this function.
		return false, nil
	})
}
