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

package crds

import (
	"context"
	"embed"
	"fmt"
	"sync"
	"time"

	crdhelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	extensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	apiextensionsv1client "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

//go:embed *.yaml
var raw embed.FS

// CreateFromFS creates the given CRDs using the target client from the
// provided filesystem and waits for it to become established. This call is blocking.
func CreateFromFS(ctx context.Context, client apiextensionsv1client.CustomResourceDefinitionInterface, fs embed.FS, grs ...metav1.GroupResource) error {
	wg := sync.WaitGroup{}
	bootstrapErrChan := make(chan error, len(grs))
	for _, gk := range grs {
		wg.Add(1)
		go func(gr metav1.GroupResource) {
			defer wg.Done()
			bootstrapErrChan <- retryRetryableErrors(func() error {
				return createSingleFromFS(ctx, client, gr, fs)
			})
		}(gk)
	}
	wg.Wait()
	close(bootstrapErrChan)
	var bootstrapErrors []error
	for err := range bootstrapErrChan {
		bootstrapErrors = append(bootstrapErrors, err)
	}
	if err := kerrors.NewAggregate(bootstrapErrors); err != nil {
		return fmt.Errorf("could not bootstrap CRDs: %w", err)
	}
	return nil
}

// Create creates the given CRDs using the target client and waits
// for all of them to become established in parallel. This call is blocking.
func Create(ctx context.Context, client apiextensionsv1client.CustomResourceDefinitionInterface, grs ...metav1.GroupResource) error {
	return CreateFromFS(ctx, client, raw, grs...)
}

// CreateFromFS creates the given CRD using the target client from the
// provided filesystem and waits for it to become established. This call is blocking.
func createSingleFromFS(ctx context.Context, client apiextensionsv1client.CustomResourceDefinitionInterface, gr metav1.GroupResource, fs embed.FS) error {
	start := time.Now()
	klog.Infof("Bootstrapping %v", gr.String())
	defer func() {
		klog.Infof("Bootstrapped %v after %s", gr.String(), time.Since(start).String())
	}()
	raw, err := fs.ReadFile(fmt.Sprintf("%s_%s.yaml", gr.Group, gr.Resource))
	if err != nil {
		return fmt.Errorf("could not read CRD %s: %w", gr.String(), err)
	}
	expectedGvk := &schema.GroupVersionKind{Group: apiextensionsv1.GroupName, Version: "v1", Kind: "CustomResourceDefinition"}
	obj, gvk, err := extensionsapiserver.Codecs.UniversalDeserializer().Decode(raw, expectedGvk, &apiextensionsv1.CustomResourceDefinition{})
	if err != nil {
		return fmt.Errorf("could not decode raw CRD %s: %w", gr.String(), err)
	}
	if !equality.Semantic.DeepEqual(gvk, expectedGvk) {
		return fmt.Errorf("decoded CRD %s into incorrect GroupVersionKind, got %#v, wanted %#v", gr.String(), gvk, expectedGvk)
	}
	rawCrd, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
	if !ok {
		return fmt.Errorf("decoded CRD %s into incorrect type, got %T, wanted %T", gr.String(), rawCrd, &apiextensionsv1.CustomResourceDefinition{})
	}

	crdResource, err := client.Get(ctx, rawCrd.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			_, err = client.Create(ctx, rawCrd, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("error creating CRD %s: %w", gr.String(), err)
			}
		} else {
			return fmt.Errorf("error fetching CRD %s: %w", gr.String(), err)
		}
	} else {
		rawCrd.ResourceVersion = crdResource.ResourceVersion
		_, err = client.Update(ctx, rawCrd, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
	}

	return wait.PollImmediateInfiniteWithContext(ctx, 100*time.Millisecond, func(ctx context.Context) (bool, error) {
		crd, err := client.Get(ctx, rawCrd.Name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, fmt.Errorf("CRD %s was deleted before being established", gr.String())
			}
			return false, fmt.Errorf("error fetching CRD %s: %w", gr.String(), err)
		}

		return crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established), nil
	})
}

func retryRetryableErrors(f func() error) error {
	return retry.OnError(retry.DefaultBackoff, func(err error) bool {
		return utilnet.IsConnectionRefused(err) || apierrors.IsTooManyRequests(err) || apierrors.IsConflict(err)
	}, f)
}
