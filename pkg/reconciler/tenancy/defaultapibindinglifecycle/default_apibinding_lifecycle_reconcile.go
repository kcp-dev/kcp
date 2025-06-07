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

package defaultapibindinglifecycle

import (
	"context"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/initialization"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
)

func (c *DefaultAPIBindingController) reconcile(ctx context.Context, logicalCluster *corev1alpha1.LogicalCluster) error {
	logger := klog.FromContext(ctx)

	annotationValue, found := logicalCluster.Annotations[tenancyv1alpha1.LogicalClusterTypeAnnotationKey]
	if !found {
		return nil
	}
	wtCluster, wtName := logicalcluster.NewPath(annotationValue).Split()
	if wtCluster.Empty() {
		return nil
	}
	var errors []error
	clusterName := logicalcluster.From(logicalCluster)
	logger.V(4).Info("reconciling default APIBindings")

	// Start with the WorkspaceType specified by the Workspace
	leafWT, err := c.getWorkspaceType(wtCluster, wtName)
	if err != nil {
		logger.Error(err, "error getting WorkspaceType")
		return nil
	}

	// Get all the transitive WorkspaceTypes
	wts, err := c.transitiveTypeResolver.Resolve(leafWT)
	if err != nil {
		logger.Error(err, "error resolving transitive types")
		return nil
	}

	// Get current bindings
	bindings, err := c.listAPIBindings(clusterName)
	if err != nil {
		errors = append(errors, err)
	}

	// This keeps track of which APIBindings have been created for which APIExports
	exportToBinding := map[apisv1alpha2.ExportBindingReference]*apisv1alpha2.APIBinding{}

	for i := range bindings {
		binding := bindings[i]

		if binding.Spec.Reference.Export == nil {
			continue
		}

		// Track what we have ("actual")
		exportToBinding[*binding.Spec.Reference.Export] = binding
	}

	requiredExportRefs := map[tenancyv1alpha1.APIExportReference]struct{}{}
	someExportsMissing := false

	for _, wt := range wts {
		if ptr.Deref(wt.Spec.DefaultAPIBindingLifecycle, tenancyv1alpha1.APIBindingLifecycleModeInitializeOnly) != tenancyv1alpha1.APIBindingLifecycleModeMaintain {
			continue
		}

		logger := logging.WithObject(logger, wt)
		logger.V(3).Info("attempting to reconcile APIBindings")

		for i := range wt.Spec.DefaultAPIBindings {
			exportRef := wt.Spec.DefaultAPIBindings[i]
			if exportRef.Path == "" {
				exportRef.Path = logicalcluster.From(wt).String()
			}
			apiExport, err := c.getAPIExport(logicalcluster.NewPath(exportRef.Path), exportRef.Export)
			if err != nil {
				if !someExportsMissing {
					errors = append(errors, fmt.Errorf("unable to complete reconciliation: unable to find at least 1 APIExport"))
				}
				someExportsMissing = true
				continue
			}

			// Keep track of unique set of expected exports across all WTs
			requiredExportRefs[exportRef] = struct{}{}

			logger := logger.WithValues("apiExport.path", exportRef.Path, "apiExport.name", exportRef.Export)
			ctx := klog.NewContext(ctx, logger)

			apiBindingName := initialization.GenerateAPIBindingName(clusterName, exportRef.Path, exportRef.Export)
			logger = logger.WithValues("apiBindingName", apiBindingName)

			apiBindingSpec := apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{
					Export: &apisv1alpha2.ExportBindingReference{
						Path: exportRef.Path,
						Name: apiExport.Name,
					},
				},
			}

			for _, exportClaim := range apiExport.Spec.PermissionClaims {
				// For now we automatically accept DefaultAPIBindings
				acceptedClaim := apisv1alpha2.AcceptablePermissionClaim{
					PermissionClaim: exportClaim,
					State:           apisv1alpha2.ClaimAccepted,
				}

				apiBindingSpec.PermissionClaims = append(apiBindingSpec.PermissionClaims, acceptedClaim)
			}

			existingBinding, err := c.getAPIBinding(clusterName, apiBindingName)
			if err != nil {
				if !apierrors.IsNotFound(err) {
					errors = append(errors, err)
					continue
				}
				if _, err := c.createAPIBinding(ctx, clusterName.Path(), &apisv1alpha2.APIBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: apiBindingName,
					},
					Spec: apiBindingSpec,
				}); err != nil {
					errors = append(errors, err)
					continue
				}
				logger.V(2).Info("created APIBinding")
			} else {
				apiBinding := existingBinding.DeepCopy()
				apiBinding.Spec = apiBindingSpec
				oldResource := &apiBindingResource{ObjectMeta: existingBinding.ObjectMeta, Spec: &existingBinding.Spec, Status: &existingBinding.Status}
				newResource := &apiBindingResource{ObjectMeta: apiBinding.ObjectMeta, Spec: &apiBinding.Spec, Status: &existingBinding.Status}
				if err := c.commitApiBinding(ctx, oldResource, newResource); err != nil {
					errors = append(errors, err)
					continue
				}
				logger.V(2).Info("updated APIBinding")
			}
		}
	}

	if len(errors) > 0 {
		logger.Error(utilerrors.NewAggregate(errors), "error reconciling APIBindings")

		if someExportsMissing {
			// Retry if any APIExports are missing, as it's possible they'll show up (cache server slow to catch up,
			// arrive via replication)
			return utilerrors.NewAggregate(errors)
		}

		return nil
	}

	// Make sure all the expected bindings are there & ready to use
	var incomplete []string

	for exportRef := range requiredExportRefs {
		binding, exists := exportToBinding[apisv1alpha2.ExportBindingReference{
			Path: exportRef.Path,
			Name: exportRef.Export,
		}]
		if !exists {
			incomplete = append(incomplete, fmt.Sprintf("for APIExport %s|%s", exportRef.Path, exportRef.Export))
			continue
		}

		if !conditions.IsTrue(binding, apisv1alpha2.InitialBindingCompleted) {
			incomplete = append(incomplete, binding.Name)
		}
	}

	if len(incomplete) > 0 {
		sort.Strings(incomplete)

		conditions.MarkFalse(
			logicalCluster,
			tenancyv1alpha1.WorkspaceAPIBindingsReconciled,
			tenancyv1alpha1.WorkspaceInitializedWaitingOnAPIBindings,
			conditionsv1alpha1.ConditionSeverityInfo,
			"APIBinding(s) not yet fully reconciled: %s",
			strings.Join(incomplete, ", "),
		)
	} else {
		conditions.MarkTrue(logicalCluster, tenancyv1alpha1.WorkspaceAPIBindingsReconciled)
	}

	logger.V(4).Info("completed default APIBinding reconciliation")
	return nil
}
