/*
Copyright 2026 The kcp Authors.

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

package identitymigrator

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
)

func TestReleaseFence(t *testing.T) {
	t.Parallel()

	fencedBy := func(owner string) *corev1alpha1.LogicalCluster {
		lc := &corev1alpha1.LogicalCluster{ObjectMeta: metav1.ObjectMeta{Name: corev1alpha1.LogicalClusterName}}
		if owner != "" {
			lc.Annotations = map[string]string{corev1alpha1.LogicalClusterInactiveAnnotationKey: owner}
		}
		return lc
	}

	scenarios := []struct {
		name           string
		bindings       []*apisv1alpha2.APIBinding
		logicalCluster *corev1alpha1.LogicalCluster
		lcErr          error
		expectLifted   bool
		expectErr      bool
	}{
		{
			name:           "fence stays while a sibling binding still needs draining",
			bindings:       []*apisv1alpha2.APIBinding{bindingOn("new", "new"), bindingOn("old", "old", "new")},
			logicalCluster: fencedBy(ControllerName),
		},
		{
			name:           "fence is lifted once every binding of the workspace is drained",
			bindings:       []*apisv1alpha2.APIBinding{bindingOn("new", "new"), bindingOn("new", "new")},
			logicalCluster: fencedBy(ControllerName),
			expectLifted:   true,
		},
		{
			name:           "fence is lifted when the draining binding was deleted",
			bindings:       nil,
			logicalCluster: fencedBy(ControllerName),
			expectLifted:   true,
		},
		{
			name:           "a fence owned by someone else is left alone",
			bindings:       []*apisv1alpha2.APIBinding{bindingOn("new", "new")},
			logicalCluster: fencedBy("workspace-migration"),
		},
		{
			name:  "a deleted workspace has no fence to lift",
			lcErr: apierrors.NewNotFound(corev1alpha1.Resource("logicalclusters"), corev1alpha1.LogicalClusterName),
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			lifted := false
			c := &Controller{
				listAPIBindingsInCluster: func(logicalcluster.Name) ([]*apisv1alpha2.APIBinding, error) {
					return scenario.bindings, nil
				},
				getLogicalCluster: func(logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
					return scenario.logicalCluster, scenario.lcErr
				},
				updateLogicalCluster: func(_ context.Context, _ logicalcluster.Path, lc *corev1alpha1.LogicalCluster) (*corev1alpha1.LogicalCluster, error) {
					if _, has := lc.Annotations[corev1alpha1.LogicalClusterInactiveAnnotationKey]; has {
						t.Errorf("expected the fence annotation to be removed, got %v", lc.Annotations)
					}
					lifted = true
					return lc, nil
				},
			}

			err := c.releaseFence(context.Background(), logicalcluster.Name("ws"))
			if (err != nil) != scenario.expectErr {
				t.Fatalf("unexpected error: %v", err)
			}
			if lifted != scenario.expectLifted {
				t.Errorf("expected lifted=%v, got %v", scenario.expectLifted, lifted)
			}
		})
	}
}
