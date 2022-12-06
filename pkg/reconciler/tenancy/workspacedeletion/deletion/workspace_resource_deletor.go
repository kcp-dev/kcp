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

/*
Copyright 2015 The Kubernetes Authors.

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

package deletion

import (
	"context"
	"fmt"
	"sort"
	"strings"

	kcpmetadata "github.com/kcp-dev/client-go/metadata"
	"github.com/kcp-dev/logicalcluster/v3"

	rbac "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/discovery"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	"github.com/kcp-dev/kcp/pkg/projection"
)

const (
	WorkspaceFinalizer = "tenancy.kcp.dev/workspace-finalizer"
)

// WorkspaceResourcesDeleterInterface is the interface to delete a workspace with all resources in it.
// This is a copy from namespace deleteor in k8s with some modification:
// - change the condition update code
// - remove opCache
// - update deleteCollection to delete resources from all namespaces
type WorkspaceResourcesDeleterInterface interface {
	Delete(ctx context.Context, ws *tenancyv1alpha1.ThisWorkspace) error
}

// NewWorkspacedResourcesDeleter returns a new NamespacedResourcesDeleter.
func NewWorkspacedResourcesDeleter(
	metadataClusterClient kcpmetadata.ClusterInterface,
	discoverResourcesFn func(clusterName logicalcluster.Path) ([]*metav1.APIResourceList, error)) WorkspaceResourcesDeleterInterface {
	d := &workspacedResourcesDeleter{
		metadataClusterClient: metadataClusterClient,
		discoverResourcesFn:   discoverResourcesFn,
	}
	return d
}

var _ WorkspaceResourcesDeleterInterface = &workspacedResourcesDeleter{}

// workspacedResourcesDeleter is used to delete all resources in a given workspace.
type workspacedResourcesDeleter struct {
	// Dynamic client to list and delete all resources in the workspace.
	metadataClusterClient kcpmetadata.ClusterInterface

	discoverResourcesFn func(clusterName logicalcluster.Path) ([]*metav1.APIResourceList, error)
}

// Delete deletes all resources in the given workspace.
// Before deleting resources:
//
// Returns ResourcesRemainingError if it deleted some resources but needs
// to wait for them to go away.
// Caller is expected to keep calling this until it succeeds.
func (d *workspacedResourcesDeleter) Delete(ctx context.Context, workspace *tenancyv1alpha1.ThisWorkspace) error {
	logger := klog.FromContext(ctx)

	// the latest view of the workspace asserts that workspace is no longer deleting..
	if workspace.DeletionTimestamp.IsZero() {
		return nil
	}

	logger.V(5).Info("deleting workspace")

	// return if it is already finalized.
	if len(workspace.Finalizers) == 0 {
		return nil
	}

	// there may still be content for us to remove
	estimate, message, err := d.deleteAllContent(ctx, workspace)
	if err != nil {
		return err
	}

	if estimate > 0 {
		return &ResourcesRemainingError{estimate, message}
	}

	return nil
}

// ResourcesRemainingError is used to inform the caller that all resources are not yet fully removed from the workspace.
type ResourcesRemainingError struct {
	Estimate int64
	Message  string
}

func (e *ResourcesRemainingError) Error() string {
	ret := fmt.Sprintf("some content remains in the workspace, estimate %d seconds before it is removed", e.Estimate)
	if e.Message == "" {
		return ret
	}
	return fmt.Sprintf("%s: %s", ret, e.Message)
}

// operation is used for caching if an operation is supported on a dynamic client.
type operation string

const (
	operationDeleteCollection operation = "deletecollection"
	operationList             operation = "list"
	// assume a default estimate for finalizers to complete when found on items pending deletion.
	finalizerEstimateSeconds int64 = int64(15)
)

// deleteCollection is a helper function that will delete the collection of resources
// it only handles cluster scoped resource.
// it returns true if the operation was supported on the server.
// it returns an error if the operation was supported on the server but was unable to complete.
func (d *workspacedResourcesDeleter) deleteCollection(ctx context.Context, clusterName logicalcluster.Path, gvr schema.GroupVersionResource, verbs sets.String) (bool, error) {
	logger := klog.FromContext(ctx).WithValues("operation", "deleteCollection", "gvr", gvr)
	logger.V(5).Info("running operation")

	if !verbs.Has(string(operationDeleteCollection)) {
		logger.V(5).Info("operation ignored since not supported")
		return false, nil
	}

	background := metav1.DeletePropagationBackground
	opts := metav1.DeleteOptions{PropagationPolicy: &background}
	if err := d.metadataClusterClient.Resource(gvr).Cluster(clusterName).DeleteCollection(
		ctx, opts, metav1.ListOptions{}); err != nil {
		logger.V(5).Error(err, "unexpected deleteCollection error")
		return true, err
	}

	return true, nil
}

// listCollection will list the items in the specified workspace
// it returns the following:
//
//	the list of items in the collection (if found)
//	a boolean if the operation is supported
//	an error if the operation is supported but could not be completed.
func (d *workspacedResourcesDeleter) listCollection(ctx context.Context, clusterName logicalcluster.Path, gvr schema.GroupVersionResource, verbs sets.String) (*metav1.PartialObjectMetadataList, bool, error) {
	logger := klog.FromContext(ctx).WithValues("operation", "listCollection", "gvr", gvr)
	logger.V(5).Info("running operation")

	if !verbs.Has(string(operationList)) {
		logger.V(5).Info("operation ignored since not supported")
		return nil, false, nil
	}

	partialList, err := d.metadataClusterClient.Cluster(clusterName).Resource(gvr).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err == nil {
		return partialList, true, nil
	}

	// this is strange, but we need to special case for both MethodNotSupported and NotFound errors
	// TODO: https://github.com/kubernetes/kubernetes/issues/22413
	// we have a resource returned in the discovery API that supports no top-level verbs:
	//  /apis/extensions/v1beta1/namespaces/default/replicationcontrollers
	// when working with this resource type, we will get a literal not found error rather than expected method not supported
	if errors.IsMethodNotSupported(err) || errors.IsNotFound(err) {
		logger.V(5).Info("operation ignored since not supported")
		return nil, false, nil
	}

	return nil, true, err
}

// deleteEachItem is a helper function that will list the collection of resources and delete each item 1 by 1.
func (d *workspacedResourcesDeleter) deleteEachItem(ctx context.Context, clusterName logicalcluster.Path, gvr schema.GroupVersionResource, verbs sets.String) error {
	logger := klog.FromContext(ctx).WithValues("operation", "deleteEachItem", "gvr", gvr)
	logger.V(5).Info("running operation")

	unstructuredList, listSupported, err := d.listCollection(ctx, clusterName, gvr, verbs)
	if err != nil {
		return err
	}
	if !listSupported {
		return nil
	}

	for _, item := range unstructuredList.Items {
		background := metav1.DeletePropagationBackground
		opts := metav1.DeleteOptions{PropagationPolicy: &background}
		if err = d.metadataClusterClient.Cluster(clusterName).Resource(gvr).Namespace(item.GetNamespace()).Delete(ctx, item.GetName(), opts); err != nil && !errors.IsNotFound(err) && !errors.IsMethodNotSupported(err) {
			return err
		}
	}
	return nil
}

type gvrDeletionMetadata struct {
	// finalizerEstimateSeconds is an estimate of how much longer to wait.  zero means that no estimate has made and does not
	// mean that all content has been removed.
	finalizerEstimateSeconds int64
	// numRemaining is how many instances of the gvr remain
	numRemaining int
	// finalizersToNumRemaining maps finalizers to how many resources are stuck on them
	finalizersToNumRemaining map[string]int
}

// deleteAllContentForGroupVersionResource will use the dynamic client to delete each resource identified in gvr.
// It returns an estimate of the time remaining before the remaining resources are deleted.
// If estimate > 0, not all resources are guaranteed to be gone.
func (d *workspacedResourcesDeleter) deleteAllContentForGroupVersionResource(
	ctx context.Context,
	clusterName logicalcluster.Path,
	gvr schema.GroupVersionResource,
	verbs sets.String,
	workspaceDeletedAt metav1.Time) (gvrDeletionMetadata, error) {
	logger := klog.FromContext(ctx).WithValues("operation", "deleteAllContentForGroupVersionResource", "gvr", gvr)
	logger.V(5).Info("running operation")

	// estimate how long it will take for the resource to be deleted (needed for objects that support graceful delete)
	estimate, err := d.estimateGracefulTermination(ctx, gvr, clusterName, workspaceDeletedAt)
	if err != nil {
		logger.V(5).Error(err, "unable to estimate")
		return gvrDeletionMetadata{}, err
	}
	logger.V(5).Info("created estimate", "estimate", estimate)

	// first try to delete the entire collection
	deleteCollectionSupported, err := d.deleteCollection(ctx, clusterName, gvr, verbs)
	if err != nil {
		return gvrDeletionMetadata{finalizerEstimateSeconds: estimate}, err
	}

	// delete collection was not supported, so we list and delete each item...
	if !deleteCollectionSupported {
		err = d.deleteEachItem(ctx, clusterName, gvr, verbs)
		if err != nil {
			return gvrDeletionMetadata{finalizerEstimateSeconds: estimate}, err
		}
	}

	// verify there are no more remaining items
	// it is not an error condition for there to be remaining items if local estimate is non-zero
	logger.V(5).Info("checking for no more items")
	unstructuredList, listSupported, err := d.listCollection(ctx, clusterName, gvr, verbs)
	if err != nil {
		logger.V(5).Error(err, "error verifying no items in workspace")
		return gvrDeletionMetadata{finalizerEstimateSeconds: estimate}, err
	}
	if !listSupported {
		return gvrDeletionMetadata{finalizerEstimateSeconds: estimate}, nil
	}
	logger.V(5).Info("items remaining", "remaining", len(unstructuredList.Items))
	if len(unstructuredList.Items) == 0 {
		// we're done
		return gvrDeletionMetadata{finalizerEstimateSeconds: 0, numRemaining: 0}, nil
	}

	// use the list to find the finalizers
	finalizersToNumRemaining := map[string]int{}
	for _, item := range unstructuredList.Items {
		for _, finalizer := range item.GetFinalizers() {
			finalizersToNumRemaining[finalizer]++
		}
	}

	if estimate != int64(0) {
		logger.V(5).Info("estimate is present", "finalizers", finalizersToNumRemaining)
		return gvrDeletionMetadata{
			finalizerEstimateSeconds: estimate,
			numRemaining:             len(unstructuredList.Items),
			finalizersToNumRemaining: finalizersToNumRemaining,
		}, nil
	}

	// if any item has a finalizer, we treat that as a normal condition, and use a default estimation to allow for GC to complete.
	if len(finalizersToNumRemaining) > 0 {
		logger.V(5).Info("items remaining with finalizers", "finalizers", finalizersToNumRemaining)
		return gvrDeletionMetadata{
			finalizerEstimateSeconds: finalizerEstimateSeconds,
			numRemaining:             len(unstructuredList.Items),
			finalizersToNumRemaining: finalizersToNumRemaining,
		}, nil
	}

	// nothing reported a finalizer, so something was unexpected as it should have been deleted.
	return gvrDeletionMetadata{
		finalizerEstimateSeconds: estimate,
		numRemaining:             len(unstructuredList.Items),
	}, fmt.Errorf("unexpected items still remain in workspace: %s for gvr: %v", clusterName, gvr)
}

type allGVRDeletionMetadata struct {
	// gvrToNumRemaining is how many instances of the gvr remain
	gvrToNumRemaining map[schema.GroupVersionResource]int
	// finalizersToNumRemaining maps finalizers to how many resources are stuck on them
	finalizersToNumRemaining map[string]int
}

// deleteAllContent will use the dynamic client to delete each resource identified in groupVersionResources.
// It returns an estimate of the time remaining before the remaining resources are deleted.
// If estimate > 0, not all resources are guaranteed to be gone.
func (d *workspacedResourcesDeleter) deleteAllContent(ctx context.Context, ws *tenancyv1alpha1.ThisWorkspace) (int64, string, error) {
	logger := klog.FromContext(ctx).WithValues("operation", "deleteAllContent")
	logger.V(5).Info("running operation")

	workspaceDeletedAt := *ws.DeletionTimestamp
	var errs []error
	estimate := int64(0)

	// disocer resources at first
	var deletionContentSuccessReason string
	resources, err := d.discoverResourcesFn(logicalcluster.From(ws))
	if err != nil {
		// discovery errors are not fatal.  We often have some set of resources we can operate against even if we don't have a complete list
		errs = append(errs, err)
		deletionContentSuccessReason = "DiscoveryFailed"
	}

	deletableResources := discovery.FilteredBy(and{
		discovery.SupportsAllVerbs{Verbs: []string{"delete"}},

		// ThisWorkspace is the trigger for the whole deletion. Don't block on it.
		isNotGroupResource{group: tenancy.GroupName, resource: "thisworkspaces"},

		// Keep the workspace accessible for users in case they have to debug.
		isNotGroupResource{group: rbac.GroupName, resource: "clusterroles"},
		isNotGroupResource{group: rbac.GroupName, resource: "clusterrolebindings"},

		// Don't try to delete projected resources such as tenancy.kcp.dev v1beta1 Workspaces - these are virtual
		// projections and we shouldn't try to delete them. The projections will disappear when the real underlying
		// data (e.g. ClusterWorkspaces) are deleted.
		isNotVirtualResource{},
		// no need to delete namespace scoped resource since it will be handled by namespace deletion anyway. This
		// can avoid redundant list/delete requests.
		isNotNamespaceScoped{},
	}, resources)
	groupVersionResources, err := groupVersionResources(deletableResources)
	if err != nil {
		// discovery errors are not fatal.  We often have some set of resources we can operate against even if we don't have a complete list
		errs = append(errs, err)
		deletionContentSuccessReason = "GroupVersionParsingFailed"
	}

	numRemainingTotals := allGVRDeletionMetadata{
		gvrToNumRemaining:        map[schema.GroupVersionResource]int{},
		finalizersToNumRemaining: map[string]int{},
	}
	deleteContentErrs := []error{}
	for gvr, verbs := range groupVersionResources {
		gvrDeletionMetadata, err := d.deleteAllContentForGroupVersionResource(ctx, logicalcluster.From(ws), gvr, verbs, workspaceDeletedAt)
		if err != nil {
			// If there is an error, hold on to it but proceed with all the remaining
			// groupVersionResources.
			deleteContentErrs = append(deleteContentErrs, err)
		}
		if gvrDeletionMetadata.finalizerEstimateSeconds > estimate {
			estimate = gvrDeletionMetadata.finalizerEstimateSeconds
		}
		if gvrDeletionMetadata.numRemaining > 0 {
			numRemainingTotals.gvrToNumRemaining[gvr] = gvrDeletionMetadata.numRemaining
			for finalizer, numRemaining := range gvrDeletionMetadata.finalizersToNumRemaining {
				if numRemaining == 0 {
					continue
				}
				numRemainingTotals.finalizersToNumRemaining[finalizer] += numRemaining
			}
		}
	}

	if len(deleteContentErrs) > 0 {
		errs = append(errs, deleteContentErrs...)
		deletionContentSuccessReason = "ContentDeletionFailed"
	}

	var contentRemainingMessages []string
	if len(numRemainingTotals.gvrToNumRemaining) != 0 {
		remainingResources := []string{}
		for gvr, numRemaining := range numRemainingTotals.gvrToNumRemaining {
			if numRemaining == 0 {
				continue
			}
			remainingResources = append(remainingResources, fmt.Sprintf("%s.%s has %d resource instances", gvr.Resource, gvr.Group, numRemaining))
		}
		// sort for stable updates
		sort.Strings(remainingResources)
		contentRemainingMessages = append(contentRemainingMessages, fmt.Sprintf("Some resources are remaining: %s", strings.Join(remainingResources, ", ")))
	}
	if len(numRemainingTotals.finalizersToNumRemaining) != 0 {
		remainingByFinalizer := []string{}
		for finalizer, numRemaining := range numRemainingTotals.finalizersToNumRemaining {
			if numRemaining == 0 {
				continue
			}
			remainingByFinalizer = append(remainingByFinalizer, fmt.Sprintf("%s in %d resource instances", finalizer, numRemaining))
		}
		// sort for stable updates
		sort.Strings(remainingByFinalizer)
		contentRemainingMessages = append(contentRemainingMessages, fmt.Sprintf("Some content in the workspace has finalizers remaining: %s", strings.Join(remainingByFinalizer, ", ")))
	}
	if len(contentRemainingMessages) > 0 {
		message := strings.Join(contentRemainingMessages, "; ")
		conditions.MarkFalse(
			ws,
			tenancyv1alpha1.WorkspaceContentDeleted,
			"SomeResourcesRemain",
			conditionsv1alpha1.ConditionSeverityInfo,
			message,
		)
		logger.V(4).Error(utilerrors.NewAggregate(errs), "resource remaining")
		return estimate, message, utilerrors.NewAggregate(errs)
	}

	if len(errs) > 0 {
		conditions.MarkFalse(
			ws,
			tenancyv1alpha1.WorkspaceContentDeleted,
			deletionContentSuccessReason,
			conditionsv1alpha1.ConditionSeverityError,
			utilerrors.NewAggregate(errs).Error(),
		)
		logger.Error(utilerrors.NewAggregate(errs), "content deletion failed", "message", deletionContentSuccessReason)
		return estimate, deletionContentSuccessReason, utilerrors.NewAggregate(errs)
	}

	conditions.MarkTrue(ws, tenancyv1alpha1.WorkspaceContentDeleted)
	return estimate, "", nil
}

// estimateGracefulTermination will estimate the graceful termination required for the specific entity in the workspace
func (d *workspacedResourcesDeleter) estimateGracefulTermination(ctx context.Context, gvr schema.GroupVersionResource, ws logicalcluster.Path, workspaceDeletedAt metav1.Time) (int64, error) {
	logger := klog.FromContext(ctx).WithValues("operation", "estimateGracefulTermination", "gvr", gvr)
	logger.V(5).Info("running operation")
	// TODO if we have any grace period for certain resources.
	estimate := int64(5)
	return estimate, nil
}

// GroupVersionResources converts APIResourceLists to the GroupVersionResources with verbs as value.
func groupVersionResources(rls []*metav1.APIResourceList) (map[schema.GroupVersionResource]sets.String, error) {
	gvrs := map[schema.GroupVersionResource]sets.String{}
	for _, rl := range rls {
		gv, err := schema.ParseGroupVersion(rl.GroupVersion)
		if err != nil {
			return nil, err
		}
		for i := range rl.APIResources {
			gvrs[schema.GroupVersionResource{Group: gv.Group, Version: gv.Version, Resource: rl.APIResources[i].Name}] = sets.NewString(rl.APIResources[i].Verbs...)
		}
	}
	return gvrs, nil
}

type isNotGroupResource struct {
	group    string
	resource string
}

func (ngr isNotGroupResource) Match(groupVersion string, r *metav1.APIResource) bool {
	comps := strings.SplitN(groupVersion, "/", 2)
	group := comps[0]
	if len(comps) == 1 {
		group = ""
	}
	return !(group == ngr.group && r.Name == ngr.resource)
}

type isNotVirtualResource struct{}

// Match checks if a resource contains all the given verbs.
func (vr isNotVirtualResource) Match(groupVersion string, r *metav1.APIResource) bool {
	gv, err := schema.ParseGroupVersion(groupVersion)
	if err != nil {
		return true
	}
	gvr := schema.GroupVersionResource{Group: gv.Group, Version: gv.Version, Resource: r.Name}
	return !projection.Includes(gvr)
}

type isNotNamespaceScoped struct{}

// Match checks if the resource is a cluster scoped resource
func (n isNotNamespaceScoped) Match(groupVersion string, r *metav1.APIResource) bool {
	return !r.Namespaced
}

type and []discovery.ResourcePredicate

func (a and) Match(groupVersion string, r *metav1.APIResource) bool {
	for _, p := range a {
		if !p.Match(groupVersion, r) {
			return false
		}
	}
	return true
}
