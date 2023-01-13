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

package committer

import (
	"context"
	"encoding/json"
	"fmt"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
)

// Resource is a generic wrapper around resources so we can generate patches.
type Resource[Sp any, St any] struct {
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              Sp `json:"spec"`
	Status            St `json:"status,omitempty"`
}

// ClusterPatcher is just the cluster-aware Patch API with a generic to keep use sites type safe.
type ClusterPatcher[R runtime.Object, P Patcher[R]] interface {
	Cluster(cluster logicalcluster.Path) P
}

// Patcher is just the Patch API with a generic to keep use sites type safe.
type Patcher[R runtime.Object] interface {
	Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (R, error)
}

// CommitFunc is an alias to clean up type declarations.
type CommitFunc[Sp any, St any] func(context.Context, *Resource[Sp, St], *Resource[Sp, St]) error

// NewCommitter returns a function that can patch instances of R based on meta,
// spec or status changes using a cluster-aware patcher.
func NewCommitter[R runtime.Object, P Patcher[R], Sp any, St any](patcher ClusterPatcher[R, P]) CommitFunc[Sp, St] {
	r := new(R)
	focusType := fmt.Sprintf("%T", *r)
	return func(ctx context.Context, old, obj *Resource[Sp, St]) error {
		return withPatchAndSubResources(ctx, focusType, old, obj,
			func(patchBytes []byte, subresources []string) error {
				clusterName := logicalcluster.From(old)
				_, err := patcher.Cluster(clusterName.Path()).Patch(ctx, obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, subresources...)
				return err
			})
	}
}

// NewCommitterScoped returns a function that can patch instances of R based on
// meta, spec or status changes using a patcher scoped to a specific cluster.
func NewCommitterScoped[R runtime.Object, P Patcher[R], Sp any, St any](patcher Patcher[R]) CommitFunc[Sp, St] {
	r := new(R)
	focusType := fmt.Sprintf("%T", *r)
	return func(ctx context.Context, old, obj *Resource[Sp, St]) error {
		return withPatchAndSubResources(ctx, focusType, old, obj,
			func(patchBytes []byte, subresources []string) error {
				_, err := patcher.Patch(ctx, obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, subresources...)
				return err
			})
	}
}

type patchFunc func([]byte, []string) error

func withPatchAndSubResources[Sp any, St any](ctx context.Context, focusType string, old, obj *Resource[Sp, St], patch patchFunc) error {
	logger := klog.FromContext(ctx)
	patchBytes, subresources, err := generatePatchAndSubResources(old, obj)
	if err != nil {
		return fmt.Errorf("failed to create patch for %s %s: %w", focusType, obj.Name, err)
	}

	if len(patchBytes) == 0 {
		return nil
	}

	logger.V(2).Info(fmt.Sprintf("patching %s", focusType), "patch", string(patchBytes))
	if err := patch(patchBytes, subresources); err != nil {
		return fmt.Errorf("failed to patch %s %s: %w", focusType, old.Name, err)
	}
	return nil
}

func generatePatchAndSubResources[Sp any, St any](old, obj *Resource[Sp, St]) ([]byte, []string, error) {
	objectMetaChanged := !equality.Semantic.DeepEqual(old.ObjectMeta, obj.ObjectMeta)
	specChanged := !equality.Semantic.DeepEqual(old.Spec, obj.Spec)
	statusChanged := !equality.Semantic.DeepEqual(old.Status, obj.Status)

	specOrObjectMetaChanged := specChanged || objectMetaChanged

	// Simultaneous updates of spec and status are never allowed.
	if specOrObjectMetaChanged && statusChanged {
		panic(fmt.Sprintf("programmer error: spec and status changed in same reconcile iteration. diff=%s", cmp.Diff(old, obj)))
	}

	if !specOrObjectMetaChanged && !statusChanged {
		return nil, nil, nil
	}

	// forPatch ensures that only the spec/objectMeta fields will be changed
	// or the status field but never both at the same time.
	forPatch := func(r *Resource[Sp, St]) *Resource[Sp, St] {
		var ret Resource[Sp, St]
		if specOrObjectMetaChanged {
			ret.ObjectMeta = r.ObjectMeta
			ret.Spec = r.Spec
		} else {
			ret.Status = r.Status
		}
		return &ret
	}

	clusterName := logicalcluster.From(old)
	name := old.Name

	oldForPatch := forPatch(old)
	// to ensure they appear in the patch as preconditions
	oldForPatch.UID = ""
	oldForPatch.ResourceVersion = ""

	oldData, err := json.Marshal(oldForPatch)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to Marshal old data for %s|%s: %w", clusterName, name, err)
	}

	newForPatch := forPatch(obj)
	// to ensure they appear in the patch as preconditions
	newForPatch.UID = old.UID
	newForPatch.ResourceVersion = old.ResourceVersion

	newData, err := json.Marshal(newForPatch)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to Marshal new data for %s|%s: %w", clusterName, name, err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create patch for %s|%s: %w", clusterName, name, err)
	}

	var subresources []string
	if statusChanged {
		subresources = []string{"status"}
	}

	return patchBytes, subresources, nil
}
