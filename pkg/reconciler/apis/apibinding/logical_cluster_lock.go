/*
Copyright 2025 The kcp Authors.

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
	"fmt"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/json"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
)

const (
	// ResourceBindingsAnnotationKey is the legacy key for the annotation on the LogicalCluster
	// to hold ResourceBindings. This is kept for backward compatibility during migration.
	//
	// Deprecated: Use the new split annotation keys instead.
	ResourceBindingsAnnotationKey = "internal.apis.kcp.io/resource-bindings"

	// LocksBindingsAnnotationKey is the annotation key for APIBinding lock entries.
	// Written by the apibinding controller.
	LocksBindingsAnnotationKey = "internal.apis.kcp.io/locks-bindings"
	// LocksCRDsAnnotationKey is the annotation key for CRD lock entries.
	// Written by the logicalclustercleanup controller.
	LocksCRDsAnnotationKey = "internal.apis.kcp.io/locks-crds"
	// LocksPendingAnnotationKey is the annotation key for pending CRD locks.
	// Written by the crdnooverlappinggvr admission plugin.
	LocksPendingAnnotationKey = "internal.apis.kcp.io/locks-pending"
)

const (
	// FieldManagerBindings is the SSA field manager for the apibinding controller.
	FieldManagerBindings = "kcp-apibinding"
	// FieldManagerCRDs is the SSA field manager for the logicalclustercleanup controller.
	FieldManagerCRDs = "kcp-logicalclustercleanup"
	// FieldManagerPending is the SSA field manager for the crdnooverlappinggvr admission plugin.
	FieldManagerPending = "kcp-crdnooverlappinggvr"
)

// Lock is a lock for a resource, part of the apis.kcp.io/resource-bindings annotation.
type Lock struct {
	// Name is the name of the APIBinding, or empty.
	Name string `json:"n,omitempty"`
	// CRD is true if the binding is for a CRD.
	CRD bool `json:"c,omitempty"`
}

// ExpirableLock is a lock that can expire CRD entries.
type ExpirableLock struct {
	Lock `json:",inline"`

	// Expiry is an optional timestamp. After that time, the CRD entry is not
	// considered valid anymore IF the object cannot be found.
	CRDExpiry *metav1.Time `json:"e,omitempty"`
}

// ResourceBindingsAnnotation is a map of "<resource>.<group>" to bindings. It
// is stored as a JSON string in the LogicalCluster annotation
// apis.kcp.io/resource-bindings. It serves as a lock for resources
// to prevent races of multiple bindings or CRDs owning the same resource.
type ResourceBindingsAnnotation map[string]ExpirableLock

// UnmarshalResourceBindingsAnnotation unmarshals JSON-formatted string
// into ResourceBindingsAnnotation map.
func UnmarshalResourceBindingsAnnotation(ann string) (ResourceBindingsAnnotation, error) {
	rbs := make(ResourceBindingsAnnotation)
	if err := json.Unmarshal([]byte(ann), &rbs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ResourceBindings annotation: %w", err)
	}
	if rbs == nil {
		rbs = make(ResourceBindingsAnnotation)
	}

	return rbs, nil
}

// GetResourceBindings reads ResourceBindingsAnnotation from LogicalCluster's annotation.
// For backward compatibility, it reads from the legacy annotation key if the new keys are not present.
//
// Deprecated: Use GetAllResourceBindings instead which merges all three annotation sources.
func GetResourceBindings(lc *corev1alpha1.LogicalCluster) (ResourceBindingsAnnotation, error) {
	const jsonEmptyObj = "{}"

	ann := lc.Annotations[ResourceBindingsAnnotationKey]
	if ann == "" {
		ann = jsonEmptyObj
	}

	return UnmarshalResourceBindingsAnnotation(ann)
}

// GetBindingsLocks reads the bindings annotation from LogicalCluster.
func GetBindingsLocks(lc *corev1alpha1.LogicalCluster) (ResourceBindingsAnnotation, error) {
	ann := lc.Annotations[LocksBindingsAnnotationKey]
	if ann == "" {
		return make(ResourceBindingsAnnotation), nil
	}
	return UnmarshalResourceBindingsAnnotation(ann)
}

// GetCRDsLocks reads the CRDs annotation from LogicalCluster.
func GetCRDsLocks(lc *corev1alpha1.LogicalCluster) (ResourceBindingsAnnotation, error) {
	ann := lc.Annotations[LocksCRDsAnnotationKey]
	if ann == "" {
		return make(ResourceBindingsAnnotation), nil
	}
	return UnmarshalResourceBindingsAnnotation(ann)
}

// GetPendingLocks reads the pending locks annotation from LogicalCluster.
func GetPendingLocks(lc *corev1alpha1.LogicalCluster) (ResourceBindingsAnnotation, error) {
	ann := lc.Annotations[LocksPendingAnnotationKey]
	if ann == "" {
		return make(ResourceBindingsAnnotation), nil
	}
	return UnmarshalResourceBindingsAnnotation(ann)
}

// GetAllResourceBindings merges all three lock annotations (crds, bindings, pending)
// into a single ResourceBindingsAnnotation with precedence: crds > bindings > pending.
// This allows readers to see a unified view while writers can use SSA on their own keys.
func GetAllResourceBindings(lc *corev1alpha1.LogicalCluster) (ResourceBindingsAnnotation, error) {
	result := make(ResourceBindingsAnnotation)

	// First, read from legacy annotation for backward compatibility during migration
	legacy, err := GetResourceBindings(lc)
	if err != nil {
		return nil, err
	}
	for k, v := range legacy {
		result[k] = v
	}

	// Then, read from new annotations with precedence: crds > bindings > pending
	// Pending locks have lowest priority (they're provisional)
	pending, err := GetPendingLocks(lc)
	if err != nil {
		return nil, err
	}
	for k, v := range pending {
		result[k] = v
	}

	// Binding locks override pending locks
	bindings, err := GetBindingsLocks(lc)
	if err != nil {
		return nil, err
	}
	for k, v := range bindings {
		result[k] = v
	}

	// CRD locks have highest priority
	crds, err := GetCRDsLocks(lc)
	if err != nil {
		return nil, err
	}
	for k, v := range crds {
		result[k] = v
	}

	return result, nil
}

// WithLockedResources tries to lock the resources for the given binding. It
// returns those resources that got successfully locked. If a resource is already
// locked by another binding, it is skipped and returned in the second return
// value.
//
// The logical cluster is not mutated.
func WithLockedResources(crds []*apiextensionsv1.CustomResourceDefinition, now time.Time, lc *corev1alpha1.LogicalCluster, grs []schema.GroupResource, binding ExpirableLock) (*corev1alpha1.LogicalCluster, []schema.GroupResource, map[schema.GroupResource]Lock, error) {
	v, found := lc.Annotations[ResourceBindingsAnnotationKey]
	if !found || v == "" {
		return nil, nil, nil, fmt.Errorf("%s annotation not found, migration has to happen first", ResourceBindingsAnnotationKey)
	}

	rbs, err := UnmarshalResourceBindingsAnnotation(v)
	if err != nil {
		return nil, nil, nil, err
	}

	crdNames := make(map[string]bool, len(crds))
	for _, crd := range crds {
		crdNames[crd.Name] = true
	}

	// find what resources need to be newly locked
	skipped := make(map[schema.GroupResource]Lock)
	newlyLocked := make([]schema.GroupResource, 0, len(grs))
	locked := make([]schema.GroupResource, 0, len(grs))
	for _, gr := range grs {
		b, found := rbs[gr.String()]
		if !found || b.Lock == binding.Lock {
			newlyLocked = append(newlyLocked, gr)
			locked = append(locked, gr)
			continue
		}
		if b.CRD && !crdNames[gr.String()] && b.CRDExpiry != nil && now.After(b.CRDExpiry.Time) {
			// CRD binding expired, and CRD is not known
			newlyLocked = append(newlyLocked, gr)
			locked = append(locked, gr)
			continue
		}
		skipped[gr] = b.Lock
	}

	// don't do anything if no resources need to be locked
	if len(newlyLocked) == 0 {
		return lc, locked, skipped, nil
	}

	// update the LogicalCluster with the new binding information
	for _, gr := range newlyLocked {
		rbs[gr.String()] = binding
	}
	lc = lc.DeepCopy()
	bs, err := json.Marshal(rbs)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to marshal ResourceBindings annotation: %w", err)
	}
	lc.Annotations[ResourceBindingsAnnotationKey] = string(bs)

	return lc, locked, skipped, nil
}

// unlockResource unlocks the resource for the given binding. It updates the
// LogicalCluster with the new binding information IFF at least one resource
// was unlocked.
func unlockResource(ctx context.Context, kcpClusterClient kcpclientset.ClusterInterface, lc *corev1alpha1.LogicalCluster, grs []schema.GroupResource, binding Lock) error { //nolint:unused // will be used eventually.
	v, found := lc.Annotations[ResourceBindingsAnnotationKey]
	if !found {
		return fmt.Errorf("%s annotation not found, migration has to happen first", ResourceBindingsAnnotationKey)
	}

	var rbs ResourceBindingsAnnotation
	if err := json.Unmarshal([]byte(v), &rbs); err != nil {
		return fmt.Errorf("failed to unmarshal ResourceBindings annotation: %w", err)
	}

	unlocked := false
	for _, gr := range grs {
		if bound, found := rbs[gr.String()]; found && bound.Lock == binding {
			delete(rbs, gr.String())
			unlocked = true
		}
	}
	if !unlocked {
		return nil
	}

	lc = lc.DeepCopy()
	bs, err := json.Marshal(rbs)
	if err != nil {
		return fmt.Errorf("failed to marshal ResourceBindings annotation: %w", err)
	}
	lc.Annotations[ResourceBindingsAnnotationKey] = string(bs)

	_, err = kcpClusterClient.CoreV1alpha1().LogicalClusters().Cluster(logicalcluster.From(lc).Path()).Update(ctx, lc, metav1.UpdateOptions{})
	if err != nil {
		return err
	}

	return nil
}

// WithLockedResourcesForBindings locks resources for the APIBinding controller.
// It reads from the merged view of all annotations but only writes to the bindings annotation.
// This allows the apibinding controller to use SSA on its own key.
func WithLockedResourcesForBindings(crds []*apiextensionsv1.CustomResourceDefinition, now time.Time, lc *corev1alpha1.LogicalCluster, grs []schema.GroupResource, binding ExpirableLock) (*corev1alpha1.LogicalCluster, []schema.GroupResource, map[schema.GroupResource]Lock, error) {
	// Read from all sources to check locks
	rbs, err := GetAllResourceBindings(lc)
	if err != nil {
		return nil, nil, nil, err
	}

	crdNames := make(map[string]bool, len(crds))
	for _, crd := range crds {
		crdNames[crd.Name] = true
	}

	// Read the bindings-specific annotation to update
	bindingsRbs, err := GetBindingsLocks(lc)
	if err != nil {
		return nil, nil, nil, err
	}

	// find what resources need to be newly locked
	skipped := make(map[schema.GroupResource]Lock)
	newlyLocked := make([]schema.GroupResource, 0, len(grs))
	locked := make([]schema.GroupResource, 0, len(grs))
	for _, gr := range grs {
		b, found := rbs[gr.String()]
		if !found || b.Lock == binding.Lock {
			newlyLocked = append(newlyLocked, gr)
			locked = append(locked, gr)
			continue
		}
		if b.CRD && !crdNames[gr.String()] && b.CRDExpiry != nil && now.After(b.CRDExpiry.Time) {
			// CRD binding expired, and CRD is not known
			newlyLocked = append(newlyLocked, gr)
			locked = append(locked, gr)
			continue
		}
		skipped[gr] = b.Lock
	}

	// don't do anything if no resources need to be locked
	if len(newlyLocked) == 0 {
		return lc, locked, skipped, nil
	}

	// update only the bindings annotation
	for _, gr := range newlyLocked {
		bindingsRbs[gr.String()] = binding
	}
	lc = lc.DeepCopy()
	bs, err := json.Marshal(bindingsRbs)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to marshal ResourceBindings annotation: %w", err)
	}
	if lc.Annotations == nil {
		lc.Annotations = make(map[string]string)
	}
	lc.Annotations[LocksBindingsAnnotationKey] = string(bs)

	return lc, locked, skipped, nil
}

// GetAllResourceBindingsForRead returns all resource bindings for reading purposes.
// This is a convenience alias for GetAllResourceBindings.
func GetAllResourceBindingsForRead(lc *corev1alpha1.LogicalCluster) (ResourceBindingsAnnotation, error) {
	return GetAllResourceBindings(lc)
}

// WithLockedResourcesForPending locks resources for the admission plugin (pending locks).
// It reads from the merged view of all annotations but only writes to the pending annotation.
// This allows the admission plugin to use SSA on its own key.
func WithLockedResourcesForPending(crds []*apiextensionsv1.CustomResourceDefinition, now time.Time, lc *corev1alpha1.LogicalCluster, grs []schema.GroupResource, binding ExpirableLock) (*corev1alpha1.LogicalCluster, []schema.GroupResource, map[schema.GroupResource]Lock, error) {
	// Read from all sources to check locks
	rbs, err := GetAllResourceBindings(lc)
	if err != nil {
		return nil, nil, nil, err
	}

	crdNames := make(map[string]bool, len(crds))
	for _, crd := range crds {
		crdNames[crd.Name] = true
	}

	// Read the pending-specific annotation to update
	pendingRbs, err := GetPendingLocks(lc)
	if err != nil {
		return nil, nil, nil, err
	}

	// find what resources need to be newly locked
	skipped := make(map[schema.GroupResource]Lock)
	newlyLocked := make([]schema.GroupResource, 0, len(grs))
	locked := make([]schema.GroupResource, 0, len(grs))
	for _, gr := range grs {
		b, found := rbs[gr.String()]
		if !found || b.Lock == binding.Lock {
			newlyLocked = append(newlyLocked, gr)
			locked = append(locked, gr)
			continue
		}
		if b.CRD && !crdNames[gr.String()] && b.CRDExpiry != nil && now.After(b.CRDExpiry.Time) {
			// CRD binding expired, and CRD is not known
			newlyLocked = append(newlyLocked, gr)
			locked = append(locked, gr)
			continue
		}
		skipped[gr] = b.Lock
	}

	// don't do anything if no resources need to be locked
	if len(newlyLocked) == 0 {
		return lc, locked, skipped, nil
	}

	// update only the pending annotation
	for _, gr := range newlyLocked {
		pendingRbs[gr.String()] = binding
	}
	lc = lc.DeepCopy()
	bs, err := json.Marshal(pendingRbs)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to marshal ResourceBindings annotation: %w", err)
	}
	if lc.Annotations == nil {
		lc.Annotations = make(map[string]string)
	}
	lc.Annotations[LocksPendingAnnotationKey] = string(bs)

	return lc, locked, skipped, nil
}
