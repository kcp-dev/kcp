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

package logicalclustermigration

import (
	"context"
	"fmt"

	"k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
)

func (c *Controller) ensureBoundCRDs(ctx context.Context, lcName logicalcluster.Name) error {
	logger := klog.FromContext(ctx)

	bindings, err := c.kcpClusterClient.Cluster(lcName.Path()).ApisV1alpha2().APIBindings().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list APIBindings in %s: %w", lcName, err)
	}

	boundCRDNames := sets.New[string]()

	for _, binding := range bindings.Items {
		if binding.Spec.Reference.Export == nil {
			continue
		}

		exportName := binding.Spec.Reference.Export.Name
		if exportName == "" {
			continue
		}

		exportPath := logicalcluster.NewPath(binding.Spec.Reference.Export.Path)
		if exportPath.Empty() {
			exportPath = logicalcluster.From(&binding).Path()
		}

		apiExport, err := c.getAPIExportByPath(exportPath, exportName)
		if err != nil {
			return fmt.Errorf("failed to get APIExport %s:%s for binding %s: %w", exportPath, exportName, binding.Name, err)
		}

		for _, resource := range apiExport.Spec.Resources {
			schema, err := c.getAPIResourceSchema(logicalcluster.From(apiExport), resource.Schema)
			if err != nil {
				return fmt.Errorf("failed to get APIResourceSchema %s in %s: %w", resource.Schema, exportPath, err)
			}

			boundCRDName := apibinding.BoundCRDName(schema)
			boundCRDNames.Insert(boundCRDName)

			// Try to get the bound CRD.
			_, err = c.getCRD(apibinding.SystemBoundCRDsClusterName, boundCRDName)
			if err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("error getting bound CRD %s: %w", boundCRDName, err)
			}
			if err == nil {
				// Bound CRD already exists. We'll go check for its status as a next step after all CRDs are created.
				continue
			}

			// Need to create bound CRD.
			crd, err := apibinding.GenerateBoundCRD(schema)
			if err != nil {
				return fmt.Errorf("failed to generate bound CRD from schema %s: %w", resource.Schema, err)
			}

			logger.V(2).Info("creating bound CRD", "crd", crd.Name, "binding", binding.Name)
			_, err = c.crdClusterClient.
				Cluster(apibinding.SystemBoundCRDsClusterName.Path()).
				ApiextensionsV1().CustomResourceDefinitions().
				Create(ctx, crd, metav1.CreateOptions{})
			if err != nil && !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("failed to create bound CRD %s: %v", crd.Name, err)
			}
		}
	}

	// Poll on all CRDs and make sure they are Established.
	for boundCRDName := range boundCRDNames {
		existingCRD, err := c.getCRD(apibinding.SystemBoundCRDsClusterName, boundCRDName)
		if err != nil {
			return fmt.Errorf("error getting bound CRD %s: %w", boundCRDName, err)
		}

		if !apihelpers.IsCRDConditionTrue(existingCRD, apiextensionsv1.Established) {
			logger.V(4).Info("bound CRD is not yet established, requeueing", "crd", boundCRDName)
			return fmt.Errorf("bound CRD %s is not yet established", boundCRDName)
		}
		if apihelpers.IsCRDConditionTrue(existingCRD, apiextensionsv1.Terminating) {
			logger.V(4).Info("bound CRD is terminating, requeueing", "crd", boundCRDName)
			return fmt.Errorf("bound CRD %s is terminating", boundCRDName)
		}
		logger.V(4).Info("bound CRD is established", "crd", boundCRDName)
	}

	return nil
}
