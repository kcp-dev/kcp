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

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kcp-dev/kcp/contrib/kube-bind/bootstrap/config/kube-bind/resources"
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

	return resources.Bootstrap(ctx, kcpClientSet, computeDiscoveryClient, computeDynamicClient, crdClient, batteriesIncluded)
}
