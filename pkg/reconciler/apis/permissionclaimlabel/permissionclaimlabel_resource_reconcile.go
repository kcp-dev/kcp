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

package permissionclaimlabel

import (
	"context"

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1/permissionclaims"
)

func (c *resourceController) reconcile(ctx context.Context, cluster logicalcluster.Name, obj *unstructured.Unstructured, gvr *schema.GroupVersionResource) error {

	// Now that we have the unstrucutred and GVR, we will need to get the all the bindings, detemine if the object needs to be updated.
	bindings, err := c.listAPIBindings(cluster)
	if err != nil {
		return err
	}

	expectedLabels, err := permissionclaims.AllClaimLabels(gvr.Group, gvr.Resource, bindings)
	if err != nil {
		return err
	}

	labels := obj.GetLabels()
	updateNeeded := false
	for k, v := range expectedLabels {
		if labels == nil {
			labels = make(map[string]string)
		}
		if value, ok := labels[k]; !ok || v != value {
			updateNeeded = true
			break
		}
	}

	if updateNeeded {
		klog.V(2).InfoS("patching object", "name", obj.GetName(), "namespace", obj.GetNamespace(), "cluster", cluster)
		return c.patchGenericObject(ctx, obj, *gvr, cluster)
	}
	return nil
}

func (c *resourceController) patchGenericObject(ctx context.Context, obj metav1.Object, gvr schema.GroupVersionResource, lc logicalcluster.Name) error {
	_, err := c.dynamicClusterClient.
		Resource(gvr).
		Namespace(obj.GetNamespace()).
		Patch(logicalcluster.WithCluster(ctx, lc), obj.GetName(), types.MergePatchType, []byte("{}"), metav1.PatchOptions{})
	// if we don't find it, and we can update lets continue on.
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	return nil
}
