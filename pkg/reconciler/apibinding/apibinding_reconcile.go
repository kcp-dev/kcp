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

package apibinding

import (
	"context"
	"encoding/json"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
)

func (c *controller) reconcile(ctx context.Context, apiBinding *apisv1alpha1.APIBinding) error {
	if apiBinding.Spec.Reference.Workspace == nil {
		return nil
	}

	workspaceRef := apiBinding.Spec.Reference.Workspace

	switch apiBinding.Status.Phase {
	case "":
		apiBinding.Status.Phase = apisv1alpha1.APIBindingPhaseBinding
	case apisv1alpha1.APIBindingPhaseBinding:
		// Determine the cluster name for the referenced export
		org, _, err := helper.ParseLogicalClusterName(apiBinding.ClusterName)
		if err != nil {
			// should never happen
			klog.Errorf("Error parsing logical cluster %q: %v", apiBinding.ClusterName, err)
			return nil // don't retry
		}

		apiExportClusterName := helper.EncodeOrganizationAndClusterWorkspace(org, workspaceRef.WorkspaceName)
		apiExport, err := c.apiExportsLister.Get(clusters.ToClusterAwareKey(apiExportClusterName, workspaceRef.ExportName))
		if err != nil {
			if apierrors.IsNotFound(err) {
				klog.Errorf(
					"Error getting APIExport %s|%s for APIBinding %s|%s: %v",
					apiExportClusterName,
					workspaceRef.ExportName,
					apiBinding.ClusterName,
					apiBinding.Name,
					err,
				)
				// TODO set condition
				return nil // don't retry
			}
		}

		var bindingErrors []error
		var boundResources []apisv1alpha1.BoundAPIResource

		for _, schemaName := range apiExport.Spec.LatestResourceSchemas {
			schema, err := c.kcpClusterClient.Cluster(apiExportClusterName).ApisV1alpha1().APIResourceSchemas().Get(ctx, schemaName, metav1.GetOptions{})
			if err != nil {
				klog.Errorf("Error getting APIResourceSchema %s|%s", apiExportClusterName, schemaName)
				// TODO set condition
				bindingErrors = append(bindingErrors, err)
				continue
			}

			crd := &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					// HACK to make Kube validation allow arbitrary names, instead of plural.group.
					Name: string(schema.UID),
					Annotations: map[string]string{
						// TODO(ncdc) consts
						"kcp.dev/schema-cluster": schema.ClusterName,
						"kcp.dev/schema-name":    schema.Name,
					},
					// TODO(ncdc) owner ref back to either schema or export?
				},
				Spec: apiextensionsv1.CustomResourceDefinitionSpec{
					Group: schema.Spec.Group,
					Names: schema.Spec.Names,
					Scope: schema.Spec.Scope,
				},
			}

			for _, version := range schema.Spec.Versions {
				crdVersion := apiextensionsv1.CustomResourceDefinitionVersion{
					Name:                     version.Name,
					Served:                   version.Served,
					Storage:                  version.Storage,
					Deprecated:               version.Deprecated,
					DeprecationWarning:       version.DeprecationWarning,
					Subresources:             version.Subresources,
					AdditionalPrinterColumns: version.AdditionalPrinterColumns,
				}

				var validation apiextensionsv1.CustomResourceValidation
				if err := json.Unmarshal(version.Schema.Raw, &validation); err != nil {
					klog.Errorf("Error decoding APIResourceSchema %s|%s version %s: %v", apiExportClusterName, schemaName, version.Name, err)
					// TODO set condition
					bindingErrors = append(bindingErrors, err)
					continue
				}
				crdVersion.Schema = &validation

				crd.Spec.Versions = append(crd.Spec.Versions, crdVersion)
			}

			// TODO handle update
			if _, err := c.crdClusterClient.Cluster(shadowWorkspaceName).ApiextensionsV1().CustomResourceDefinitions().Create(ctx, crd, metav1.CreateOptions{}); err != nil {
				if !apierrors.IsAlreadyExists(err) {
					klog.Errorf("Error creating CRD %s|%s: %v", shadowWorkspaceName, crd.Name, err)
					bindingErrors = append(bindingErrors, err)
					continue
				}
			}

			boundResources = append(boundResources, apisv1alpha1.BoundAPIResource{
				Group:    schema.Spec.Group,
				Resource: schema.Spec.Names.Plural,
				Schema: apisv1alpha1.BoundAPIResourceSchema{
					Name: schema.Name,
					UID:  string(schema.UID),
				},
				StorageVersions: []string{"TODO"},
			})
		}

		if len(bindingErrors) == 0 {
			apiBinding.Status.Initializers = []string{}
			apiBinding.Status.Phase = apisv1alpha1.APIBindingPhaseBound
			apiBinding.Status.BoundAPIExport = &apiBinding.Spec.Reference
			apiBinding.Status.BoundResources = boundResources
		}
	}

	return nil
}
