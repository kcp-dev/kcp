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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	kubeadmission "k8s.io/apiserver/pkg/admission"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	apisv1alpha2informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"

	"github.com/kcp-dev/kcp/pkg/virtual/framework/admission"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
)

type selectorAdmission struct {
	getAPIBindingByExport func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error)
}

// NewSelectorAdmission builds admission functions that can mutate and validate incoming requests to
// add labels from the label selector on objects claimed via permission claims.
func NewSelectorAdmission(apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer, kubeClusterClient kcpkubernetesclientset.ClusterInterface) admission.MutatorValidator {
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

func (s *selectorAdmission) Admit(ctx context.Context, a kubeadmission.Attributes, o kubeadmission.ObjectInterfaces) error {
	if a.GetOperation() != kubeadmission.Create && a.GetOperation() != kubeadmission.Update {
		// We consider only CREATE and UPDATE requests
		return nil
	}

	targetCluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return kubeadmission.NewForbidden(a, fmt.Errorf("error getting valid cluster from context: %w", err))
	}

	if targetCluster.Wildcard || a.GetResource().Resource == "" {
		// if the target is the wildcard cluster or it's a non-resource URL request,
		// we can skip checking the APIBinding in the target cluster.
		return nil
	}

	apiDomainKey := dynamiccontext.APIDomainKeyFrom(ctx)
	parts := strings.Split(string(apiDomainKey), "/")
	if len(parts) < 2 {
		return kubeadmission.NewForbidden(a, fmt.Errorf("invalid API domain key"))
	}
	apiExportCluster, apiExportName := parts[0], parts[1]

	apiBinding, err := s.getAPIBindingByExport(targetCluster.Name.String(), apiExportName, apiExportCluster)
	if err != nil {
		return kubeadmission.NewForbidden(a, fmt.Errorf("could not find suitable apibinding in target logical cluster: %w", err))
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

		// if we find the resource by its group/resource
		if permissionClaim.Group == a.GetResource().Group && permissionClaim.Resource == a.GetResource().Resource {
			// if permissionClaim is matchAll, nothing to do
			if permissionClaim.Selector.MatchAll {
				return nil
			}

			// get labels from the object that's being mutated
			u, ok := a.GetObject().(*unstructured.Unstructured)
			if !ok {
				return kubeadmission.NewForbidden(a, fmt.Errorf("unexpected type %T", a.GetObject()))
			}

			lbls := u.GetLabels()
			if lbls == nil {
				lbls = map[string]string{}
			}

			// apply labels from matchLabels if not present
			if len(permissionClaim.Selector.MatchLabels) > 0 {
				for expectedKey, expectedVal := range permissionClaim.Selector.MatchLabels {
					if currVal, ok := lbls[expectedKey]; !ok {
						// this means that the key doesn't exist, set it to expected value
						lbls[expectedKey] = expectedVal
					} else if ok && currVal != expectedVal {
						// this means that the key exist but has different value, return an error
						// because we consider it a protected key
						return kubeadmission.NewForbidden(a, fmt.Errorf("protected label %s must have value %s", expectedKey, expectedVal))
					}
				}
			}

			// matchExpressions are not applied on the object intentionally
			// because we can't properly determine what labels should be applied

			u.SetLabels(lbls)

			// it's safe to return here because we can't have multiple permission claims
			// for the same group/resource
			return nil
		}
	}

	return nil
}

func (s *selectorAdmission) Validate(ctx context.Context, a kubeadmission.Attributes, o kubeadmission.ObjectInterfaces) error {
	switch a.GetOperation() {
	case kubeadmission.Create:
	case kubeadmission.Update:
	case kubeadmission.Delete:
	default:
		// We consider only CREATE, UPDATE and DELETE requests
		return nil
	}

	targetCluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return kubeadmission.NewForbidden(a, fmt.Errorf("error getting valid cluster from context: %w", err))
	}

	if targetCluster.Wildcard || a.GetResource().Resource == "" {
		// if the target is the wildcard cluster or it's a non-resource URL request,
		// we can skip checking the APIBinding in the target cluster.
		return nil
	}

	apiDomainKey := dynamiccontext.APIDomainKeyFrom(ctx)
	parts := strings.Split(string(apiDomainKey), "/")
	if len(parts) < 2 {
		return kubeadmission.NewForbidden(a, fmt.Errorf("invalid API domain key"))
	}
	apiExportCluster, apiExportName := parts[0], parts[1]

	apiBinding, err := s.getAPIBindingByExport(targetCluster.Name.String(), apiExportName, apiExportCluster)
	if err != nil {
		return kubeadmission.NewForbidden(a, fmt.Errorf("could not find suitable apibinding in target logical cluster: %w", err))
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

		// if we find the resource by its group/resource
		if permissionClaim.Group == a.GetResource().Group && permissionClaim.Resource == a.GetResource().Resource {
			// if permissionClaim is matchAll, nothing to do
			if permissionClaim.Selector.MatchAll {
				return nil
			}

			// otherwise, get labels and do validation
			obj := a.GetObject()
			if obj == nil {
				obj = a.GetOldObject()
			}
			u, ok := obj.(*unstructured.Unstructured)
			if !ok {
				return kubeadmission.NewForbidden(a, fmt.Errorf("unexpected type %T", obj))
			}

			lbls := u.GetLabels()
			if lbls == nil {
				lbls = map[string]string{}
			}

			selector, err := metav1.LabelSelectorAsSelector(&permissionClaim.Selector.LabelSelector)
			if err != nil {
				return kubeadmission.NewForbidden(a, fmt.Errorf("error building selector from provided label selector: %w", err))
			}

			if !selector.Matches(labels.Set(lbls)) {
				// return fmt.Errorf("object does not match required labels")
				return kubeadmission.NewForbidden(a, fmt.Errorf("object does not match required labels"))
			}

			// it's safe to return here because we can't have multiple permission claims
			// for the same group/resource
			return nil
		}
	}

	return nil
}
