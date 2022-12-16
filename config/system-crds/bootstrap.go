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

package systemcrds

import (
	"context"
	"embed"
	"fmt"
	"time"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	confighelpers "github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/pkg/apis/apis"
	"github.com/kcp-dev/kcp/pkg/apis/core"
)

//go:embed *.yaml
var fs embed.FS

// Bootstrap creates CRDs and the resources in this package by continuously retrying the list.
// This is blocking, i.e. it only returns (with error) when the context is closed or with nil when
// the bootstrapping is successfully completed.
func Bootstrap(ctx context.Context, crdClient apiextensionsclient.Interface, discoveryClient discovery.DiscoveryInterface, dynamicClient dynamic.Interface, batteriesIncluded sets.String) error {
	logger := klog.FromContext(ctx)
	// This is the full list of CRDs that kcp owns and manages in the system:system-crds logical cluster. Our custom CRD
	// lister currently has a hard-coded list of which system CRDs are made available to which workspaces. See
	// pkg/server/apiextensions.go newSystemCRDProvider for the list. These CRDs should never be installed in any other
	// logical cluster.
	// TODO(sttts): get rid of this and enforce/support schema evolution while allowing wildcard informers to work
	crds := []metav1.GroupResource{
		{Group: apis.GroupName, Resource: "apiexports"},
		{Group: apis.GroupName, Resource: "apibindings"},
		{Group: apis.GroupName, Resource: "apiresourceschemas"},
		{Group: apis.GroupName, Resource: "apiexportendpointslices"},
		{Group: core.GroupName, Resource: "logicalclusters"},
	}

	if err := wait.PollImmediateInfiniteWithContext(ctx, time.Second, func(ctx context.Context) (bool, error) {
		if err := configcrds.Create(ctx, crdClient.ApiextensionsV1().CustomResourceDefinitions(), crds...); err != nil {
			logger.Error(err, "failed to bootstrap system CRDs, retrying")
			return false, nil // keep retrying
		}
		return true, nil
	}); err != nil {
		return fmt.Errorf("failed to bootstrap system CRDs: %w", err)
	}

	return confighelpers.Bootstrap(ctx, discoveryClient, dynamicClient, batteriesIncluded, fs)
}
