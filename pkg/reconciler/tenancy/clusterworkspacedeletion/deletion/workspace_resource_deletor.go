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

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/metadata"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
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
	Delete(ctx context.Context, ws *tenancyv1alpha1.ClusterWorkspace) error
}

// NewWorkspacedResourcesDeleter returns a new NamespacedResourcesDeleter.
func NewWorkspacedResourcesDeleter(
	metadataClusterClient metadata.Interface,
	discoverResourcesFn func(clusterName logicalcluster.Name) ([]*metav1.APIResourceList, error)) WorkspaceResourcesDeleterInterface {
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
	metadataClusterClient metadata.Interface

	discoverResourcesFn func(clusterName logicalcluster.Name) ([]*metav1.APIResourceList, error)
}

// Delete deletes all resources in the given workspace.
// Before deleting resources:
//
// Returns ResourcesRemainingError if it deleted some resources but needs
// to wait for them to go away.
// Caller is expected to keep calling this until it succeeds.
func (d *workspacedResourcesDeleter) Delete(ctx context.Context, workspace *tenancyv1alpha1.ClusterWorkspace) error {
	var err error
	// if the workspace was deleted already, don't do anything
	if workspace.DeletionTimestamp == nil {
		return nil
	}

	klog.V(5).Infof("workspace deletion controller - syncworkspace - workspace: %s, finalizer: %s", workspace.Name, WorkspaceFinalizer)

	// the latest view of the workspace asserts that workspace is no longer deleting..
	if workspace.DeletionTimestamp.IsZero() {
		return nil
	}

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
// it returns true if the operation was supported on the server.
// it returns an error if the operation was supported on the server but was unable to complete.
func (d *workspacedResourcesDeleter) deleteCollection(ctx context.Context, clusterName logicalcluster.Name, gvr schema.GroupVersionResource, verbs sets.String) (bool, error) {
	klog.V(5).Infof("workspace deletion controller - deleteCollection - workspace: %s, gvr: %v", clusterName, gvr)

	if !verbs.Has(string(operationDeleteCollection)) {
		klog.V(5).Infof("workspace deletion controller - deleteCollection ignored since not supported - workspace: %s, gvr: %v", clusterName, gvr)
		return false, nil
	}

	unstructuredList, listSupported, err := d.listCollection(ctx, clusterName, gvr, verbs)
	if err != nil {
		return false, err
	}
	if !listSupported {
		return false, nil
	}

	deletedNamespaces := sets.String{}
	deleteErrors := []error{}
	for _, item := range unstructuredList.Items {
		if deletedNamespaces.Has(item.GetNamespace()) {
			continue
		}
		// don't retry deleting the same namespace
		deletedNamespaces.Insert(item.GetNamespace())
		background := metav1.DeletePropagationBackground
		opts := metav1.DeleteOptions{PropagationPolicy: &background}
		if err := d.metadataClusterClient.Resource(gvr).Namespace(item.GetNamespace()).DeleteCollection(
			logicalcluster.WithCluster(ctx, clusterName), opts, metav1.ListOptions{}); err != nil {
			deleteErrors = append(deleteErrors, err)
			continue
		}
	}

	deleteError := utilerrors.NewAggregate(deleteErrors)

	if deleteError == nil {
		return true, nil
	}

	klog.V(5).Infof("workspace deletion controller - deleteCollection unexpected error - workspace: %s, gvr: %v, error: %v", clusterName, gvr, deleteError)
	return true, deleteError
}

// listCollection will list the items in the specified workspace
// it returns the following:
//  the list of items in the collection (if found)
//  a boolean if the operation is supported
//  an error if the operation is supported but could not be completed.
func (d *workspacedResourcesDeleter) listCollection(ctx context.Context, clusterName logicalcluster.Name, gvr schema.GroupVersionResource, verbs sets.String) (*metav1.PartialObjectMetadataList, bool, error) {
	klog.V(5).Infof("workspace deletion controller - listCollection - workspace: %s, gvr: %v", clusterName, gvr)

	if !verbs.Has(string(operationList)) {
		klog.V(5).Infof("workspace deletion controller - listCollection ignored since not supported - workspace: %s, gvr: %v", clusterName, gvr)
		return nil, false, nil
	}

	partialList, err := d.metadataClusterClient.Resource(gvr).Namespace(metav1.NamespaceAll).List(logicalcluster.WithCluster(ctx, clusterName), metav1.ListOptions{})
	if err == nil {
		return partialList, true, nil
	}

	// this is strange, but we need to special case for both MethodNotSupported and NotFound errors
	// TODO: https://github.com/kubernetes/kubernetes/issues/22413
	// we have a resource returned in the discovery API that supports no top-level verbs:
	//  /apis/extensions/v1beta1/namespaces/default/replicationcontrollers
	// when working with this resource type, we will get a literal not found error rather than expected method not supported
	if errors.IsMethodNotSupported(err) || errors.IsNotFound(err) {
		klog.V(5).Infof("workspace deletion controller - listCollection not supported - workspace: %s, gvr: %v", clusterName, gvr)
		return nil, false, nil
	}

	return nil, true, err
}

// deleteEachItem is a helper function that will list the collection of resources and delete each item 1 by 1.
func (d *workspacedResourcesDeleter) deleteEachItem(ctx context.Context, clusterName logicalcluster.Name, gvr schema.GroupVersionResource, verbs sets.String) error {
	klog.V(5).Infof("workspace deletion controller - deleteEachItem - workspace: %s, gvr: %v", clusterName, gvr)

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
		if err = d.metadataClusterClient.Resource(gvr).Namespace(item.GetNamespace()).Delete(logicalcluster.WithCluster(ctx, clusterName), item.GetName(), opts); err != nil && !errors.IsNotFound(err) && !errors.IsMethodNotSupported(err) {
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
	clusterName logicalcluster.Name,
	gvr schema.GroupVersionResource,
	verbs sets.String,
	workspaceDeletedAt metav1.Time) (gvrDeletionMetadata, error) {
	klog.V(5).Infof("workspace deletion controller - deleteAllContentForGroupVersionResource - workspace: %s, gvr: %v", clusterName, gvr)

	// estimate how long it will take for the resource to be deleted (needed for objects that support graceful delete)
	estimate, err := d.estimateGracefulTermination(gvr, clusterName, workspaceDeletedAt)
	if err != nil {
		klog.V(5).Infof("workspace deletion controller - deleteAllContentForGroupVersionResource - unable to estimate - workspace: %s, gvr: %v, err: %v", clusterName, gvr, err)
		return gvrDeletionMetadata{}, err
	}
	klog.V(5).Infof("workspace deletion controller - deleteAllContentForGroupVersionResource - estimate - workspace: %s, gvr: %v, estimate: %v", clusterName, gvr, estimate)

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
	klog.V(5).Infof("workspace deletion controller - deleteAllContentForGroupVersionResource - checking for no more items in workspace: %s, gvr: %v", clusterName, gvr)
	unstructuredList, listSupported, err := d.listCollection(ctx, clusterName, gvr, verbs)
	if err != nil {
		klog.V(5).Infof("workspace deletion controller - deleteAllContentForGroupVersionResource - error verifying no items in workspace: %s, gvr: %v, err: %v", clusterName, gvr, err)
		return gvrDeletionMetadata{finalizerEstimateSeconds: estimate}, err
	}
	if !listSupported {
		return gvrDeletionMetadata{finalizerEstimateSeconds: estimate}, nil
	}
	klog.V(5).Infof("workspace deletion controller - deleteAllContentForGroupVersionResource - items remaining - workspace: %s, gvr: %v, items: %v", clusterName, gvr, len(unstructuredList.Items))
	if len(unstructuredList.Items) == 0 {
		// we're done
		return gvrDeletionMetadata{finalizerEstimateSeconds: 0, numRemaining: 0}, nil
	}

	// use the list to find the finalizers
	finalizersToNumRemaining := map[string]int{}
	for _, item := range unstructuredList.Items {
		for _, finalizer := range item.GetFinalizers() {
			finalizersToNumRemaining[finalizer] = finalizersToNumRemaining[finalizer] + 1
		}
	}

	if estimate != int64(0) {
		klog.V(5).Infof("workspace deletion controller - deleteAllContentForGroupVersionResource - estimate is present - workspace: %s, gvr: %v, finalizers: %v", clusterName, gvr, finalizersToNumRemaining)
		return gvrDeletionMetadata{
			finalizerEstimateSeconds: estimate,
			numRemaining:             len(unstructuredList.Items),
			finalizersToNumRemaining: finalizersToNumRemaining,
		}, nil
	}

	// if any item has a finalizer, we treat that as a normal condition, and use a default estimation to allow for GC to complete.
	if len(finalizersToNumRemaining) > 0 {
		klog.V(5).Infof("workspace deletion controller - deleteAllContentForGroupVersionResource - items remaining with finalizers - workspace: %s, gvr: %v, finalizers: %v", clusterName, gvr, finalizersToNumRemaining)
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
func (d *workspacedResourcesDeleter) deleteAllContent(ctx context.Context, ws *tenancyv1alpha1.ClusterWorkspace) (int64, string, error) {
	workspace := ws.Name
	workspaceDeletedAt := *ws.DeletionTimestamp
	var errs []error
	estimate := int64(0)
	klog.V(4).Infof("workspace deletion controller - deleteAllContent - workspace: %s", workspace)

	wsClusterName := logicalcluster.From(ws).Join(ws.Name)

	// disocer resources at first
	var (
		deletionContentSuccessReason  string
		deletionContentSuccessMessage string
	)
	resources, err := d.discoverResourcesFn(wsClusterName)
	if err != nil {
		// discovery errors are not fatal.  We often have some set of resources we can operate against even if we don't have a complete list
		errs = append(errs, err)

		deletionContentSuccessReason = "DiscoveryFailed"
		deletionContentSuccessMessage = err.Error()
	}

	deletableResources := discovery.FilteredBy(and{
		discovery.SupportsAllVerbs{Verbs: []string{"delete"}},
		isNotVirtualResource{},
	}, resources)
	groupVersionResources, err := groupVersionResources(deletableResources)
	if err != nil {
		// discovery errors are not fatal.  We often have some set of resources we can operate against even if we don't have a complete list
		errs = append(errs, err)

		deletionContentSuccessReason = "GroupVersionParsingFailed"
		deletionContentSuccessMessage = err.Error()
	}

	numRemainingTotals := allGVRDeletionMetadata{
		gvrToNumRemaining:        map[schema.GroupVersionResource]int{},
		finalizersToNumRemaining: map[string]int{},
	}
	deleteContentErrs := []error{}
	for gvr, verbs := range groupVersionResources {
		gvrDeletionMetadata, err := d.deleteAllContentForGroupVersionResource(ctx, wsClusterName, gvr, verbs, workspaceDeletedAt)
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
				numRemainingTotals.finalizersToNumRemaining[finalizer] = numRemainingTotals.finalizersToNumRemaining[finalizer] + numRemaining
			}
		}
	}

	if len(deleteContentErrs) > 0 {
		errs = append(errs, deleteContentErrs...)

		deletionContentSuccessReason = "ContentDeletionFailed"
		deletionContentSuccessMessage = utilerrors.NewAggregate(deleteContentErrs).Error()
	}

	if deletionContentSuccessReason == "" {
		conditions.MarkTrue(ws, tenancyv1alpha1.WorkspaceDeletionContentSuccess)
	} else {
		conditions.MarkFalse(
			ws,
			tenancyv1alpha1.WorkspaceDeletionContentSuccess,
			deletionContentSuccessReason,
			conditionsv1alpha1.ConditionSeverityError,
			deletionContentSuccessMessage,
		)
	}

	var contentDeletedMessages []string
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
		contentDeletedMessages = append(contentDeletedMessages, fmt.Sprintf("Some resources are remaining: %s", strings.Join(remainingResources, ", ")))
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
		contentDeletedMessages = append(contentDeletedMessages, fmt.Sprintf("Some content in the workspace has finalizers remaining: %s", strings.Join(remainingByFinalizer, ", ")))
	}

	message := ""
	if len(contentDeletedMessages) > 0 {
		message = strings.Join(contentDeletedMessages, "; ")
		conditions.MarkFalse(
			ws,
			tenancyv1alpha1.WorkspaceContentDeleted,
			"SomeResourcesRemain",
			conditionsv1alpha1.ConditionSeverityError,
			message,
		)
	} else {
		conditions.MarkTrue(ws, tenancyv1alpha1.WorkspaceContentDeleted)
	}

	klog.V(4).Infof("workspace deletion controller - deleteAllContent - workspace: %s, estimate: %v, errors: %v", wsClusterName, estimate, utilerrors.NewAggregate(errs))
	return estimate, message, utilerrors.NewAggregate(errs)
}

// estimateGracefulTermination will estimate the graceful termination required for the specific entity in the workspace
func (d *workspacedResourcesDeleter) estimateGracefulTermination(gvr schema.GroupVersionResource, ws logicalcluster.Name, workspaceDeletedAt metav1.Time) (int64, error) {
	groupResource := gvr.GroupResource()
	klog.V(5).Infof("workspace deletion controller - estimateGracefulTermination - group %s, resource: %s", groupResource.Group, groupResource.Resource)
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

// virtualResources are those that are mapped into a workspace, but are not actually the
// backing resource. This can be because the backing resource is in the same workspace,
// it can be that the source of the resource is another workspace.
// TODO(sttts): this is a hack, there should be serious mechanism to map in resources into workspaces.
var virtualResources = sets.NewString(
	"workspaces.tenancy.kcp.dev",
)

type isNotVirtualResource struct{}

// Match checks if a resource contains all the given verbs.
func (vr isNotVirtualResource) Match(groupVersion string, r *metav1.APIResource) bool {
	gv, err := schema.ParseGroupVersion(groupVersion)
	if err != nil {
		return true
	}
	gr := metav1.GroupResource{Group: gv.Group, Resource: r.Name}
	return !virtualResources.Has(gr.String())
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
