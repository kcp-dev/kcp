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

	"github.com/kcp-dev/logicalcluster/v2"

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
			err := retryRetryableErrors(func() error {
				return createSingleFromFS(ctx, client, gr, fs)
			})
			// wait.Poll functions return ErrWaitTimeout instead the context cancellation error, for backward compatibility reasons, see:
			// https://github.com/kubernetes/kubernetes/blob/b5f8cca701575678819b5e9e6372df989ab6799f/staging/src/k8s.io/apimachinery/pkg/util/wait/wait.go
			// however, retryOnError swallows that error and replaces it for the last one, that is nil if it is still retrying, see:
			// https://github.com/kubernetes/kubernetes/blob/ee81e5ebfad1b3f3c1112e7b83b0a5113286a3d3/pkg/client/unversioned/util.go
			// if the context is cancelled, we have to inform the upper layers about that, so context error takes precedence.
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			bootstrapErrChan <- err
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

	crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
	if !ok {
		return fmt.Errorf("decoded CRD %s into incorrect type, got %T, wanted %T", gr.String(), crd, &apiextensionsv1.CustomResourceDefinition{})
	}

	return CreateSingle(ctx, client, crd)
}

func CreateSingle(ctx context.Context, client apiextensionsv1client.CustomResourceDefinitionInterface, rawCRD *apiextensionsv1.CustomResourceDefinition) error {
	start := time.Now()
	klog.V(4).Infof("Bootstrapping %v", rawCRD.Name)

	updateNeeded := false
	crd, err := client.Get(ctx, rawCRD.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			crd, err = client.Create(ctx, rawCRD, metav1.CreateOptions{})
			if err != nil {
				// If multiple post-start hooks specify the same CRD, they could race with each other, so we need to
				// handle the scenario where another hook created this CRD after our Get() call returned not found.
				if apierrors.IsAlreadyExists(err) {
					// Re-get so we have the correct resourceVersion
					crd, err = client.Get(ctx, rawCRD.Name, metav1.GetOptions{})
					if err != nil {
						return fmt.Errorf("error getting CRD %s: %w", rawCRD.Name, err)
					}
					updateNeeded = true
				} else {
					return fmt.Errorf("error creating CRD %s: %w", rawCRD.Name, err)
				}
			} else {
				klog.Infof("Bootstrapped CRD %s|%v after %s", logicalcluster.From(crd), crd.Name, time.Since(start).String())
			}
		} else {
			return fmt.Errorf("error fetching CRD %s: %w", rawCRD.Name, err)
		}
	} else {
		updateNeeded = true
	}

	if updateNeeded {
		rawCRD.ResourceVersion = crd.ResourceVersion
		crd, err := client.Update(ctx, rawCRD, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
		klog.Infof("Updated CRD %s|%v after %s", logicalcluster.From(crd), rawCRD.Name, time.Since(start).String())
	}

	return wait.PollImmediateInfiniteWithContext(ctx, 100*time.Millisecond, func(ctx context.Context) (bool, error) {
		crd, err := client.Get(ctx, rawCRD.Name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, fmt.Errorf("CRD %s was deleted before being established", rawCRD.Name)
			}
			return false, fmt.Errorf("error fetching CRD %s: %w", rawCRD.Name, err)
		}

		return crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established), nil
	})
}

func retryRetryableErrors(f func() error) error {
	return retry.OnError(retry.DefaultBackoff, func(err error) bool {
		return utilnet.IsConnectionRefused(err) || apierrors.IsTooManyRequests(err) || apierrors.IsConflict(err)
	}, f)
}
