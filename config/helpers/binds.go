/*
Copyright 2026 The kcp Authors.

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
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclient "github.com/kcp-dev/sdk/client/clientset/versioned"

	"github.com/kcp-dev/kcp/pkg/errgroup"
	"github.com/kcp-dev/kcp/pkg/logging"
)

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
