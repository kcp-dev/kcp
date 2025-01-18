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

package apibinding

import (
	"context"
	"fmt"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/json"

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
)

const (
	// ResourceBindingsAnnotationKey is the key for the annotation on the LogicalCluster
	// to hold ResourceBindings.
	ResourceBindingsAnnotationKey = "internal.apis.kcp.io/resource-bindings"
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

	rbs := make(ResourceBindingsAnnotation)
	if err := json.Unmarshal([]byte(v), &rbs); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to unmarshal ResourceBindings annotation: %w", err)
	}
	if rbs == nil {
		rbs = make(ResourceBindingsAnnotation)
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
func unlockResource(ctx context.Context, kcpClusterCLient kcpclientset.ClusterInterface, lc *corev1alpha1.LogicalCluster, grs []schema.GroupResource, binding Lock) error { //nolint:unused // will be used eventually.
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

	_, err = kcpClusterCLient.CoreV1alpha1().LogicalClusters().Cluster(logicalcluster.From(lc).Path()).Update(ctx, lc, metav1.UpdateOptions{})
	if err != nil {
		return err
	}

	return nil
}
