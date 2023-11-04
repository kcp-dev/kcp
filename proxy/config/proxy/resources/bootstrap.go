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

package resources

import (
	"context"
	"embed"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"

	confighelpers "github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/pkg/logging"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	core "github.com/kcp-dev/kcp/sdk/apis/core"
	kcpclient "github.com/kcp-dev/kcp/sdk/client/clientset/versioned"
	kcpclientcluster "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
)

//go:embed *.yaml
var KubeFS embed.FS

// Bootstrap creates resources in this package by continuously retrying the list.
// This is blocking, i.e. it only returns (with error) when the context is closed or with nil when
// the bootstrapping is successfully completed.
// TODO: comment bellow does not make sense, needs porting from tmc
// Note: Any change to the list of resources in the kubernetes apiexport has to be kept consistent with:
//   - pkg/reconciler/workload/apiexport/workload_apiexport_reconcile.go
func Bootstrap(ctx context.Context, kcpClient kcpclientcluster.ClusterInterface, discoveryClient discovery.DiscoveryInterface, dynamicClient dynamic.Interface, batteriesIncluded sets.Set[string]) error {
	export, err := kcpClient.ApisV1alpha1().APIExports().Cluster(core.RootCluster.Path()).Get(ctx, "shards.core.kcp.io", metav1.GetOptions{})
	if err != nil {
		return err
	}

	err = confighelpers.Bootstrap(ctx, discoveryClient, dynamicClient, batteriesIncluded, KubeFS, confighelpers.ReplaceOption(
		"CORE_IDENTITY_HASH", export.Status.IdentityHash,
	))
	if err != nil {
		return err
	}

	if err := BindProxyToRootAPIs(ctx, kcpClient.Cluster(core.RootCluster.Path()), export.Status.IdentityHash, "proxy.kcp.io"); err != nil {
		return err
	}

	return nil
}

func BindProxyToRootAPIs(ctx context.Context, kcpClient kcpclient.Interface, identity string, exportNames ...string) error {
	logger := klog.FromContext(ctx)

	for _, exportName := range exportNames {
		binding := &apisv1alpha1.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: exportName,
			},
			Spec: apisv1alpha1.APIBindingSpec{
				Reference: apisv1alpha1.BindingReference{
					Export: &apisv1alpha1.ExportBindingReference{
						Path: logicalcluster.NewPath("root:proxy").String(),
						Name: exportName,
					},
				},
				PermissionClaims: []apisv1alpha1.AcceptablePermissionClaim{
					{
						PermissionClaim: apisv1alpha1.PermissionClaim{
							All: true,
							GroupResource: apisv1alpha1.GroupResource{
								Group:    core.GroupName,
								Resource: "shards",
							},
							IdentityHash: identity,
						},
						State: apisv1alpha1.ClaimAccepted,
					},
				},
			},
		}

		spew.Dump(binding)

		created, err := kcpClient.ApisV1alpha1().APIBindings().Create(ctx, binding, metav1.CreateOptions{})
		if err == nil {
			logger := logging.WithObject(logger, created)
			logger.V(2).Info("Created API binding")
			continue
		}
		if !apierrors.IsAlreadyExists(err) {
			return err
		}

		if err := wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
			existing, err := kcpClient.ApisV1alpha1().APIBindings().Get(ctx, exportName, metav1.GetOptions{})
			if err != nil {
				logger.Error(err, "error getting APIBinding", "name", exportName)
				// Always keep trying. Don't ever return an error out of this function.
				return false, nil
			}

			logger := logging.WithObject(logger, existing)
			logger.V(2).Info("Updating API binding")

			existing.Spec = binding.Spec

			_, err = kcpClient.ApisV1alpha1().APIBindings().Update(ctx, existing, metav1.UpdateOptions{})
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
		}); err != nil {
			return err
		}
	}

	return nil
}
