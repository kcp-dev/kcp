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
	"fmt"
	"reflect"
	"strings"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/endpoints/handlers"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

func (c *resourceController) reconcile(ctx context.Context, obj *unstructured.Unstructured, gvr *schema.GroupVersionResource) error {
	logger := klog.FromContext(ctx)

	clusterName := logicalcluster.From(obj)
	expectedLabels, _, err := c.permissionClaimLabeler.LabelsFor(ctx, clusterName, gvr.GroupResource(), obj.GetName(), obj.GetNamespace())
	if err != nil {
		return fmt.Errorf("error calculating permission claim labels for GVR %q %s/%s: %w", gvr, obj.GetNamespace(), obj.GetName(), err)
	}

	actualClaimLabels := make(map[string]string)
	for k, v := range obj.GetLabels() {
		if strings.HasPrefix(k, apisv1alpha1.APIExportPermissionClaimLabelPrefix) {
			actualClaimLabels[k] = v
		}
	}

	if (len(expectedLabels) == 0 && len(actualClaimLabels) == 0) || reflect.DeepEqual(expectedLabels, actualClaimLabels) {
		return nil
	}

	actualGVR := *gvr
	if actualVersion := obj.GetAnnotations()[handlers.KCPOriginalAPIVersionAnnotation]; actualVersion != "" {
		actualGV, err := schema.ParseGroupVersion(actualVersion)
		if err != nil {
			logger.Error(err, "error parsing original API version annotation", "annotation", actualVersion)
			// Returning an error and reprocessing will presumably result in the same parse error, so just return
			// nil here.
			return nil
		}
		actualGVR.Version = actualGV.Version
		logger.V(4).Info("using actual API version from annotation", "actual", actualVersion)
	}

	logger.V(2).Info("patch needed",
		"expectedClaimLabels", expectedLabels,
		"actualClaimLabels", actualClaimLabels,
		"diff", cmp.Diff(actualClaimLabels, expectedLabels),
		"actualGVR", actualGVR,
	)
	_, err = c.dynamicClusterClient.
		Cluster(clusterName.Path()).
		Resource(actualGVR).
		Namespace(obj.GetNamespace()).
		Patch(ctx, obj.GetName(), types.MergePatchType, []byte("{}"), metav1.PatchOptions{})

	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(2).Info("got a not found error when trying to patch")
			return nil
		}

		return fmt.Errorf("error patching GVR %q %s/%s: %w", actualGVR, obj.GetNamespace(), obj.GetName(), err)
	}

	return nil
}
