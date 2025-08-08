/*
Copyright 2025 The KCP Authors.

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

package admission

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	kubeadmission "k8s.io/apiserver/pkg/admission"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/virtual/framework/admission"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	apisv1alpha2informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha2"
)

type selectorAdmission struct {
	getAPIBindingByExport func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error)
}

func NewSelectorAdmission(apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer, kubeClusterClient kcpkubernetesclientset.ClusterInterface) admission.MutationInterface {
	apiBindingLister := apiBindingInformer.Lister()

	return &selectorAdmission{
		getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
			bindings, err := apiBindingLister.Cluster(logicalcluster.Name(clusterName)).List(labels.Everything())
			if err != nil {
				return nil, err
			}

			for _, binding := range bindings {
				if binding == nil {
					continue
				}

				if binding.Spec.Reference.Export != nil && binding.Spec.Reference.Export.Name == apiExportName && binding.Status.APIExportClusterName == apiExportCluster {
					return binding, nil
				}
			}

			return nil, fmt.Errorf("no suitable binding found")
		},
	}
}

// TODO(xmdurii): fix comments
func (s *selectorAdmission) Admit(ctx context.Context, a kubeadmission.Attributes, o kubeadmission.ObjectInterfaces) error {
	targetCluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return fmt.Errorf("error getting valid cluster from context: %w", err)
	}

	if targetCluster.Wildcard || a.GetResource().Resource == "" {
		// if the target is the wildcard cluster or it's a non-resource URL request,
		// we can skip checking the APIBinding in the target cluster.
		return nil
	}

	apiDomainKey := dynamiccontext.APIDomainKeyFrom(ctx)
	parts := strings.Split(string(apiDomainKey), "/")
	if len(parts) < 2 {
		return fmt.Errorf("invalid API domain key")
	}
	apiExportCluster, apiExportName := parts[0], parts[1]

	apiBinding, err := s.getAPIBindingByExport(targetCluster.Name.String(), apiExportName, apiExportCluster)
	if err != nil {
		return nil
		// return authorizer.DecisionDeny, "could not find suitable APIBinding in target logical cluster", nil //nolint:nilerr // this is on purpose, we want to deny, not return a server error
	}

	// check if request is for a bound resource.
	for _, resource := range apiBinding.Status.BoundResources {
		if resource.Group == a.GetResource().Group && resource.Resource == a.GetResource().Resource {
			return nil
		}
	}

	for _, permissionClaim := range apiBinding.Spec.PermissionClaims {
		if permissionClaim.State != apisv1alpha2.ClaimAccepted {
			// if the claim is not accepted it cannot be used.
			continue
		}

		if permissionClaim.Group == a.GetResource().Group && permissionClaim.Resource == a.GetResource().Resource {
			if permissionClaim.Selector.MatchAll {
				return nil
			}
			if len(permissionClaim.Selector.MatchExpressions) != 0 {
				return nil
			}
			if len(permissionClaim.Selector.MatchLabels) == 0 {
				return fmt.Errorf("this is not supposed to happen")
			}

			u, ok := a.GetObject().(*unstructured.Unstructured)
			if !ok {
				return fmt.Errorf("unexpected type %T", a.GetObject())
			}

			lbls := u.GetLabels()
			if lbls == nil {
				lbls = map[string]string{}
			}

			for k, v := range permissionClaim.Selector.MatchLabels {
				lbls[k] = v
			}

			u.SetLabels(lbls)
		}
	}

	return nil
}
