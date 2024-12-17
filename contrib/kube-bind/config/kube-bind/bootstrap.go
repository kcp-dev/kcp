/*
Copyright 2023 The KCP Authors.

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

package kubebind

import (
	"context"
	"time"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/sdk/client/clientset/versioned"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/logicalcluster/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kcp-dev/kcp/contrib/kube-bind/bootstrap/config/kube-bind/resources"
	"k8s.io/klog/v2"
)

var (
	// RootClusterName is the workspace to host common APIs.
	RootClusterName = logicalcluster.NewPath("root:kube-bind")
)

// Bootstrap creates resources in this package by continuously retrying the list.
// This is blocking, i.e. it only returns (with error) when the context is closed or with nil when
// the bootstrapping is successfully completed.
func Bootstrap(
	ctx context.Context,
	kcpClientSet kcpclientset.ClusterInterface,
	apiExtensionClusterClient kcpapiextensionsclientset.ClusterInterface,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	batteriesIncluded sets.Set[string],
) error {
	computeDiscoveryClient := apiExtensionClusterClient.Cluster(RootClusterName).Discovery()
	computeDynamicClient := dynamicClusterClient.Cluster(RootClusterName)

	crdClient := apiExtensionClusterClient.ApiextensionsV1().Cluster(RootClusterName).CustomResourceDefinitions()
	kcpClient := kcpClientSet.Cluster(RootClusterName)

	err := resources.Bootstrap(ctx, kcpClientSet, computeDiscoveryClient, computeDynamicClient, crdClient, batteriesIncluded)
	if err != nil {
		return err
	}

	// create recursive apibinding so we can start controllers.
	// this is a temporary solution until we have a better way to bootstrap controllers.
	return bindAPIExport(ctx, kcpClient, "kube-bind.io", RootClusterName)
}

func bindAPIExport(ctx context.Context, kcpClient kcpclient.Interface, exportName string, clusterPath logicalcluster.Path) error {
	logger := klog.FromContext(ctx)

	binding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: exportName,
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Path: clusterPath.String(),
					Name: exportName,
				},
			},
		},
	}

	binding.Spec.PermissionClaims = []apisv1alpha1.AcceptablePermissionClaim{
		{
			PermissionClaim: apisv1alpha1.PermissionClaim{
				All: true,
				GroupResource: apisv1alpha1.GroupResource{
					Group:    "rbac.authorization.k8s.io",
					Resource: "clusterrolebindings",
				},
			},
			State: apisv1alpha1.ClaimAccepted,
		},
		{
			PermissionClaim: apisv1alpha1.PermissionClaim{
				All: true,
				GroupResource: apisv1alpha1.GroupResource{
					Group:    "rbac.authorization.k8s.io",
					Resource: "clusterroles",
				},
			},
			State: apisv1alpha1.ClaimAccepted,
		},
		{
			PermissionClaim: apisv1alpha1.PermissionClaim{
				All: true,
				GroupResource: apisv1alpha1.GroupResource{
					Group:    "",
					Resource: "serviceaccounts",
				},
			},
			State: apisv1alpha1.ClaimAccepted,
		},
		{
			PermissionClaim: apisv1alpha1.PermissionClaim{
				All: true,
				GroupResource: apisv1alpha1.GroupResource{
					Group:    "",
					Resource: "configmaps",
				},
			},
			State: apisv1alpha1.ClaimAccepted,
		},
		{
			PermissionClaim: apisv1alpha1.PermissionClaim{
				All: true,
				GroupResource: apisv1alpha1.GroupResource{
					Group:    "",
					Resource: "secrets",
				},
			},
			State: apisv1alpha1.ClaimAccepted,
		},
	}

	_, err := kcpClient.ApisV1alpha1().APIBindings().Create(ctx, binding, metav1.CreateOptions{})
	if err == nil {
		return nil
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

	return nil
}
