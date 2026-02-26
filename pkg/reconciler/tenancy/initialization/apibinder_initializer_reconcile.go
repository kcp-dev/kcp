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

package initialization

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math/big"
	"reflect"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/sdk/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"

	"github.com/kcp-dev/kcp/pkg/logging"
)

func (b *APIBinder) reconcile(ctx context.Context, logicalCluster *corev1alpha1.LogicalCluster) error {
	annotationValue, found := logicalCluster.Annotations[tenancyv1alpha1.LogicalClusterTypeAnnotationKey]
	if !found {
		return nil
	}
	wtCluster, wtName := logicalcluster.NewPath(annotationValue).Split()
	if wtCluster.Empty() {
		return nil
	}
	logger := klog.FromContext(ctx).WithValues(
		"workspacetype.path", wtCluster.String(),
		"workspacetype.name", wtName,
	)

	var errors []error
	clusterName := logicalcluster.From(logicalCluster)
	logger.V(3).Info("initializing APIBindings for workspace")

	// Start with the WorkspaceType specified by the Workspace
	leafWT, err := b.getWorkspaceType(wtCluster, wtName)
	if err != nil {
		logger.Error(err, "error getting WorkspaceType")

		conditions.MarkFalse(
			logicalCluster,
			tenancyv1alpha1.WorkspaceAPIBindingsInitialized,
			tenancyv1alpha1.WorkspaceInitializedWorkspaceTypeInvalid,
			conditionsv1alpha1.ConditionSeverityError,
			"error getting WorkspaceType %s|%s: %v",
			wtCluster.String(), wtName, err,
		)

		return err
	}

	// Get all the transitive WorkspaceTypes
	wts, err := b.transitiveTypeResolver.Resolve(leafWT)
	if err != nil {
		logger.Error(err, "error resolving transitive types")

		conditions.MarkFalse(
			logicalCluster,
			tenancyv1alpha1.WorkspaceAPIBindingsInitialized,
			tenancyv1alpha1.WorkspaceInitializedWorkspaceTypeInvalid,
			conditionsv1alpha1.ConditionSeverityError,
			"error resolving transitive set of workspace types: %v",
			err,
		)

		return err
	}

	// Get current bindings
	bindings, err := b.listAPIBindings(clusterName)
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
		logger := logging.WithObject(logger, wt)
		logger.V(3).Info("attempting to initialize APIBindings")

		for i := range wt.Spec.DefaultAPIBindings {
			exportRef := wt.Spec.DefaultAPIBindings[i]
			if exportRef.Path == "" {
				exportRef.Path = logicalcluster.From(wt).String()
			}
			apiExport, err := b.getAPIExport(logicalcluster.NewPath(exportRef.Path), exportRef.Export)
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

			existingBinding, err := b.getAPIBinding(clusterName, apiBindingName)
			if err != nil && !apierrors.IsNotFound(err) {
				errors = append(errors, err)
				continue
			}

			apiBinding := &apisv1alpha2.APIBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: apiBindingName,
				},
				Spec: apisv1alpha2.APIBindingSpec{
					Reference: apisv1alpha2.BindingReference{
						Export: &apisv1alpha2.ExportBindingReference{
							Path: exportRef.Path,
							Name: apiExport.Name,
						},
					},
				},
			}

			apiBindingClaimsFailed := false
			for _, exportClaim := range apiExport.Spec.PermissionClaims {
				var selector apisv1alpha2.PermissionClaimSelector
				selector = apisv1alpha2.PermissionClaimSelector{
					MatchAll: true,
				}

				var parentPath logicalcluster.Path
				if annPath, found := logicalCluster.Annotations[core.LogicalClusterPathAnnotationKey]; found {
					currentPath := logicalcluster.NewPath(annPath)
					parentPath, _ = currentPath.Parent()
					logger.V(3).Info("determined parent path from annotation", "currentPath", currentPath, "parentPath", parentPath)
				} else if logicalcluster.From(logicalCluster) != core.RootCluster {
					err := fmt.Errorf("LogicalCluster %s is missing %q annotation, cannot determine parent path for selector inheritance", logicalCluster.Name, core.LogicalClusterPathAnnotationKey)
					logger.V(2).Info(err.Error())
					errors = append(errors, err)
					apiBindingClaimsFailed = true
					break
				} else {
					logger.V(3).Info("Root cluster detected, no parent path to inherit from")
				}

				if !parentPath.Empty() {
					parentSelector, err := b.findSelectorInWorkspace(ctx, parentPath, exportRef, exportClaim)
					if err != nil {
						errors = append(errors, err)
						apiBindingClaimsFailed = true
						break
					}
					if parentSelector != nil {
						selector = *parentSelector
						logger.V(3).Info("inheriting selector from parent workspace binding", "parentPath", parentPath, "selector", selector)
					}
				}

				acceptedClaim := apisv1alpha2.AcceptablePermissionClaim{
					ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
						PermissionClaim: exportClaim,
						Selector:        selector,
					},
					State: apisv1alpha2.ClaimAccepted,
				}

				apiBinding.Spec.PermissionClaims = append(apiBinding.Spec.PermissionClaims, acceptedClaim)
			}

			if apiBindingClaimsFailed {
				continue
			}

			if existingBinding != nil {
				if reflect.DeepEqual(existingBinding.Spec.PermissionClaims, apiBinding.Spec.PermissionClaims) {
					logger.V(4).Info("APIBinding already exists and is up-to-date - skipping")
					continue
				}

				apiBinding.ObjectMeta = existingBinding.ObjectMeta
				logger.V(2).Info("updating existing APIBinding")
				if _, err := b.updateAPIBinding(ctx, clusterName.Path(), apiBinding); err != nil {
					errors = append(errors, err)
					continue
				}
				logger.V(2).Info("updated APIBinding")
			} else {
				logger.V(2).Info("trying to create APIBinding")
				if _, err := b.createAPIBinding(ctx, clusterName.Path(), apiBinding); err != nil {
					if apierrors.IsAlreadyExists(err) {
						logger.V(2).Info("APIBinding already exists")
						continue
					}

					errors = append(errors, err)
					continue
				}
				logger.V(2).Info("created APIBinding")
			}
		}
	}

	if len(errors) > 0 {
		logger.Error(utilerrors.NewAggregate(errors), "error initializing APIBindings")

		conditions.MarkFalse(
			logicalCluster,
			tenancyv1alpha1.WorkspaceAPIBindingsInitialized,
			tenancyv1alpha1.WorkspaceInitializedAPIBindingErrors,
			conditionsv1alpha1.ConditionSeverityError,
			"encountered errors: %v",
			utilerrors.NewAggregate(errors),
		)

		return utilerrors.NewAggregate(errors)
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
			tenancyv1alpha1.WorkspaceAPIBindingsInitialized,
			tenancyv1alpha1.WorkspaceInitializedWaitingOnAPIBindings,
			conditionsv1alpha1.ConditionSeverityInfo,
			"APIBinding(s) not yet fully bound: %s",
			strings.Join(incomplete, ", "),
		)

		return nil
	}

	logicalCluster.Status.Initializers = initialization.EnsureInitializerAbsent(tenancyv1alpha1.WorkspaceAPIBindingsInitializer, logicalCluster.Status.Initializers)

	return nil
}

func (b *APIBinder) findSelectorInWorkspace(ctx context.Context, workspacePath logicalcluster.Path, exportRef tenancyv1alpha1.APIExportReference, exportClaim apisv1alpha2.PermissionClaim) (*apisv1alpha2.PermissionClaimSelector, error) {
	logger := klog.FromContext(ctx)

	if workspacePath.Empty() {
		logger.V(4).Info("workspacePath is empty, skipping selector lookup")
		return nil, nil
	}

	logger.V(4).Info("looking up selector in workspace", "workspacePath", workspacePath, "exportReference", exportRef.Path+"|"+exportRef.Export)
	workspaceBindings, err := b.listAPIBindingsByPath(ctx, workspacePath)
	if err != nil {
		logger.V(4).Info("error listing workspace APIBindings by path", "error", err, "path", workspacePath)
		return nil, fmt.Errorf("error listing parent workspace %q APIBindings: %w", workspacePath, err)
	}

	exportRefPath := logicalcluster.NewPath(exportRef.Path)
	if exportRefPath.Empty() {
		exportRefPath = workspacePath
	}

	var matchingBindings []*apisv1alpha2.APIBinding
	for _, binding := range workspaceBindings {
		if binding.Spec.Reference.Export == nil {
			continue
		}

		bindingExportPath := logicalcluster.NewPath(binding.Spec.Reference.Export.Path)
		if bindingExportPath.Empty() {
			bindingExportPath = workspacePath
		}

		if binding.Spec.Reference.Export.Name == exportRef.Export &&
			bindingExportPath.String() == exportRefPath.String() {
			matchingBindings = append(matchingBindings, binding)
		}
	}

	if len(matchingBindings) == 0 {
		return nil, fmt.Errorf("no APIBindings found in parent workspace %q for APIExport %s|%s", workspacePath, exportRef.Path, exportRef.Export)
	}

	var matchedSelector *apisv1alpha2.PermissionClaimSelector
	for _, binding := range matchingBindings {
		for _, claim := range binding.Spec.PermissionClaims {
			if claim.Group == exportClaim.Group &&
				claim.Resource == exportClaim.Resource &&
				claim.IdentityHash == exportClaim.IdentityHash &&
				claim.State == apisv1alpha2.ClaimAccepted {
				if !claim.Selector.MatchAll {
					logger.V(4).Info("found matching selector in workspace binding", "workspacePath", workspacePath, "selector", claim.Selector)
					return &claim.Selector, nil
				}

				if matchedSelector == nil {
					matchedSelector = &claim.Selector
				}
			}
		}
	}

	if matchedSelector != nil {
		logger.V(4).Info("found matching selector in workspace binding", "workspacePath", workspacePath, "selector", matchedSelector)
		return matchedSelector, nil
	}

	return nil, fmt.Errorf("no matching permission claim found in parent workspace %q APIBindings for APIExport %s|%s", workspacePath, exportRef.Path, exportRef.Export)
}

// maxExportNamePrefixLength is the maximum allowed length for the export name portion of the generated API binding
// name. Subtrace 1 for the dash ("-") that separates the export name prefix from the hash suffix, and 5 for the
// hash length.
const maxExportNamePrefixLength = validation.DNS1123SubdomainMaxLength - 1 - 5

func GenerateAPIBindingName(clusterName logicalcluster.Name, exportPath, exportName string) string {
	return generateAPIBindingName(clusterName, exportPath, exportName)
}

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
