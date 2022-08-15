package postreconcile

import (
	"context"
	"encoding/json"
	"fmt"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster/v2"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
)

// Resource is a generic wrapper around resources so we can generate patches
type Resource[Sp any, St any] struct {
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              Sp `json:"spec"`
	Status            St `json:"status,omitempty"`
}

// Patcher is just the Patch API with a generic to keep use sites type safe
type Patcher[R any] interface {
	Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (R, error)
}

// NewPostReconciler returns a function that can patch instances of R based on spec or status changes.
func NewPostReconcile[R any, Sp any, St any](patcher Patcher[R]) func(context.Context, *Resource[Sp, St], *Resource[Sp, St]) error {
	focusType := fmt.Sprintf("%T", *new(R))
	return func(ctx context.Context, old, obj *Resource[Sp, St]) error {
		logger := klog.FromContext(ctx)

		specChanged := !equality.Semantic.DeepEqual(old.Spec, obj.Spec)
		objectMetaChanged := !equality.Semantic.DeepEqual(old.ObjectMeta, obj.ObjectMeta)
		statusChanged := !equality.Semantic.DeepEqual(old.Status, obj.Status)

		specOrObjectMetaChanged := specChanged || objectMetaChanged

		if !specOrObjectMetaChanged && !statusChanged {
			return nil
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
			return fmt.Errorf("failed to Marshal old data for %s %s|%s: %w", focusType, clusterName, name, err)
		}

		newForPatch := forPatch(obj)
		// to ensure they appear in the patch as preconditions
		newForPatch.UID = old.UID
		newForPatch.ResourceVersion = old.ResourceVersion

		newData, err := json.Marshal(newForPatch)
		if err != nil {
			return fmt.Errorf("failed to Marshal new data for %s %s|%s: %w", focusType, clusterName, name, err)
		}

		patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
		if err != nil {
			return fmt.Errorf("failed to create patch for %s %s|%s: %w", focusType, clusterName, name, err)
		}

		var subresources []string
		if statusChanged {
			subresources = []string{"status"}
		}

		logger.V(2).Info(fmt.Sprintf("patching %s", focusType), "patch", string(patchBytes))
		_, err = patcher.Patch(logicalcluster.WithCluster(ctx, clusterName), obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, subresources...)
		if err != nil {
			return fmt.Errorf("failed to patch %s %s|%s: %w", focusType, clusterName, name, err)
		}

		// Simultaneous updates of spec and status are never allowed.
		if specOrObjectMetaChanged && statusChanged {
			panic(fmt.Sprintf("programmer error: spec and status changed in same reconcile iteration. diff=%s", cmp.Diff(old, obj)))
		}

		return nil
	}
}
