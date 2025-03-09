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
	"crypto/sha256"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
)

const DefaultAPIBindingLifecycleInitializeOnly string = "InitializeOnly"
const DefaultAPIBindingLifecycleMaintain string = "Maintain"

func (c *DefaultAPIBindingController) reconcile(ctx context.Context, logicalCluster *corev1alpha1.LogicalCluster) error {
	lifecycle, found := logicalCluster.Annotations[tenancyv1alpha1.ExperimentalDefaultAPIBindingLifecycleAnnotationKey]
	if !found || lifecycle != DefaultAPIBindingLifecycleMaintain {
		return nil
	}

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
	exportToBinding := map[apisv1alpha1.ExportBindingReference]*apisv1alpha1.APIBinding{}

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
					errors = append(errors, fmt.Errorf("unable to complete initialization: unable to find at least 1 APIExport"))
				}
				someExportsMissing = true
				continue
			}

			// Keep track of unique set of expected exports across all WTs
			requiredExportRefs[exportRef] = struct{}{}

			logger := logger.WithValues("apiExport.path", exportRef.Path, "apiExport.name", exportRef.Export)
			ctx := klog.NewContext(ctx, logger)

			apiBindingName := generateAPIBindingName(clusterName, exportRef.Path, exportRef.Export)
			logger = logger.WithValues("apiBindingName", apiBindingName)

			if _, err = c.getAPIBinding(clusterName, apiBindingName); err == nil {
				logger.V(4).Info("APIBinding already exists - skipping creation")
				continue
			}

			apiBinding := &apisv1alpha1.APIBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: apiBindingName,
				},
				Spec: apisv1alpha1.APIBindingSpec{
					Reference: apisv1alpha1.BindingReference{
						Export: &apisv1alpha1.ExportBindingReference{
							Path: exportRef.Path,
							Name: apiExport.Name,
						},
					},
				},
			}

			for i := range apiExport.Spec.PermissionClaims {
				exportClaim := apiExport.Spec.PermissionClaims[i]

				// TODO(blut): accept default api bindings automatically in Maintenance mode -> to be discussed
				acceptedClaim := apisv1alpha1.AcceptablePermissionClaim{
					PermissionClaim: exportClaim,
					State:           apisv1alpha1.ClaimAccepted,
				}

				apiBinding.Spec.PermissionClaims = append(apiBinding.Spec.PermissionClaims, acceptedClaim)
			}

			logger = logging.WithObject(logger, apiBinding)

			// TODO: should reconcile
			logger.V(2).Info("trying to create APIBinding")
			if _, err := c.createAPIBinding(ctx, clusterName.Path(), apiBinding); err != nil {
				if apierrors.IsAlreadyExists(err) {
					logger.V(2).Info("APIBinding already exists")
					continue
				}

				errors = append(errors, err)
				continue
			}

			logger.V(2).Info("created APIBinding")
		}
		// Maintained APIBindings werden geschuetzt, Orphaned Bindings koennen geloescht werden
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
		binding, exists := exportToBinding[apisv1alpha1.ExportBindingReference{
			Path: exportRef.Path,
			Name: exportRef.Export,
		}]
		if !exists {
			incomplete = append(incomplete, fmt.Sprintf("for APIExport %s|%s", exportRef.Path, exportRef.Export))
			continue
		}

		if !conditions.IsTrue(binding, apisv1alpha1.InitialBindingCompleted) {
			incomplete = append(incomplete, binding.Name)
		}
	}

	if len(incomplete) > 0 {
		sort.Strings(incomplete)
		return nil
	}

	logger.V(4).Info("completed default APIBinding reconciliation")
	return nil
}

// maxExportNamePrefixLength is the maximum allowed length for the export name portion of the generated API binding
// name. Subtrace 1 for the dash ("-") that separates the export name prefix from the hash suffix, and 5 for the
// hash length.
const maxExportNamePrefixLength = validation.DNS1123SubdomainMaxLength - 1 - 5

func generateAPIBindingName(clusterName logicalcluster.Name, exportPath, exportName string) string {
	maxLen := len(exportName)
	if maxLen > maxExportNamePrefixLength {
		maxLen = maxExportNamePrefixLength
	}

	exportNamePrefix := exportName[:maxLen]

	hash := toBase36Sha224(
		clusterName.String() + "|" + exportPath + "|" + exportName,
	)

	hash = hash[0:5]
	// Have to lowercase because Kubernetes names must be lowercase
	hash = strings.ToLower(hash)

	return fmt.Sprintf("%s-%s", exportNamePrefix, hash)
}

func toBase36(hash [28]byte) string {
	var i big.Int
	i.SetBytes(hash[:])
	return i.Text(62)
}

func toBase36Sha224(s string) string {
	return toBase36(sha256.Sum224([]byte(s)))
}
