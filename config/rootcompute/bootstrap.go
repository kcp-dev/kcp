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

package rootcompute

import (
	"context"
	"embed"

	"github.com/kcp-dev/logicalcluster/v2"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/dynamic"

	confighelpers "github.com/kcp-dev/kcp/config/helpers"
	kube124 "github.com/kcp-dev/kcp/config/rootcompute/kube-1.24"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

//go:embed *.yaml
var fs embed.FS

// Bootstrap creates resources in this package by continuously retrying the list.
// This is blocking, i.e. it only returns (with error) when the context is closed or with nil when
// the bootstrapping is successfully completed.
func Bootstrap(ctx context.Context, apiExtensionClusterClient apiextensionsclient.ClusterInterface, dynamicClusterClient dynamic.ClusterInterface, batteriesIncluded sets.String) error {
	rootDiscoveryClient := apiExtensionClusterClient.Cluster(tenancyv1alpha1.RootCluster).Discovery()
	rootDynamicClient := dynamicClusterClient.Cluster(tenancyv1alpha1.RootCluster)
	if err := confighelpers.Bootstrap(ctx, rootDiscoveryClient, rootDynamicClient, batteriesIncluded, fs); err != nil {
		return err
	}

	computeWorkspace := logicalcluster.New("root:compute")
	computeDiscoveryClient := apiExtensionClusterClient.Cluster(computeWorkspace).Discovery()
	computeDynamicClient := dynamicClusterClient.Cluster(computeWorkspace)

	return kube124.Bootstrap(ctx, computeDiscoveryClient, computeDynamicClient, batteriesIncluded)
}
