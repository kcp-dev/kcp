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

package defaultapibindinglifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/workqueue"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"

	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
)

func newTestController(cached *corev1alpha1.LogicalCluster, update func(context.Context, *corev1alpha1.LogicalCluster) error) *DefaultAPIBindingController {
	c := &DefaultAPIBindingController{
		getLogicalCluster: func(logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
			return cached, nil
		},
		getLogicalClusterByPath: func(logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
			return nil, apierrors.NewNotFound(schema.GroupResource{}, "")
		},
		getWorkspaceType: func(logicalcluster.Path, string) (*tenancyv1alpha1.WorkspaceType, error) {
			return &tenancyv1alpha1.WorkspaceType{}, nil
		},
		listLogicalClusters: func() ([]*corev1alpha1.LogicalCluster, error) { return nil, nil },
		listAPIBindings: func(logicalcluster.Name) ([]*apisv1alpha2.APIBinding, error) {
			return nil, nil
		},
		getAPIBinding: func(logicalcluster.Name, string) (*apisv1alpha2.APIBinding, error) {
			return nil, apierrors.NewNotFound(schema.GroupResource{}, "")
		},
		createAPIBinding: func(context.Context, logicalcluster.Path, *apisv1alpha2.APIBinding) (*apisv1alpha2.APIBinding, error) {
			return nil, nil
		},
		getAPIExport: func(logicalcluster.Path, string) (*apisv1alpha2.APIExport, error) {
			return nil, apierrors.NewNotFound(schema.GroupResource{}, "")
		},
		commitApiBinding: func(context.Context, *apiBindingResource, *apiBindingResource) error {
			return nil
		},
		commitConditions: committer.NewConditionsCommitter[*corev1alpha1.LogicalCluster](
			[]conditionsv1alpha1.ConditionType{tenancyv1alpha1.WorkspaceAPIBindingsReconciled}, update),
		transitiveTypeResolver: &noopResolver{},
	}
	return c
}

func testCachedLogicalCluster() *corev1alpha1.LogicalCluster {
	return &corev1alpha1.LogicalCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:            corev1alpha1.LogicalClusterName,
			ResourceVersion: "1",
			Annotations: map[string]string{
				tenancyv1alpha1.LogicalClusterTypeAnnotationKey: "root:universal",
			},
		},
	}
}

func TestProcessConditionIsolation(t *testing.T) {
	t.Parallel()

	// The cached object already carries a condition written by a different controller
	// (WorkspaceAPIBindingsInitialized). After our controller runs, that foreign condition
	// must still be present on the object we submit.
	cached := testCachedLogicalCluster()
	conditions.Set(cached, &conditionsv1alpha1.Condition{
		Type:               tenancyv1alpha1.WorkspaceAPIBindingsInitialized,
		Status:             corev1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
	})

	var updateStatusCalled bool
	var receivedLC *corev1alpha1.LogicalCluster
	c := newTestController(cached, func(_ context.Context, lc *corev1alpha1.LogicalCluster) error {
		updateStatusCalled = true
		receivedLC = lc.DeepCopy()
		return nil
	})

	err := c.process(context.Background(), "root:ws|cluster")
	require.NoError(t, err)

	require.True(t, updateStatusCalled, "updateLCStatus should have been called")

	// The condition this controller owns must be present.
	ownedCond := conditions.Get(receivedLC, tenancyv1alpha1.WorkspaceAPIBindingsReconciled)
	require.NotNil(t, ownedCond, "owned condition WorkspaceAPIBindingsReconciled must be set")

	// The foreign condition must not have been removed.
	foreignCond := conditions.Get(receivedLC, tenancyv1alpha1.WorkspaceAPIBindingsInitialized)
	require.NotNil(t, foreignCond, "foreign condition must be preserved")
	require.Equal(t, corev1.ConditionTrue, foreignCond.Status)
}

func TestProcessConflictRequeues(t *testing.T) {
	t.Parallel()

	c := newTestController(testCachedLogicalCluster(), func(_ context.Context, _ *corev1alpha1.LogicalCluster) error {
		return apierrors.NewConflict(corev1alpha1.Resource("logicalclusters"), corev1alpha1.LogicalClusterName, errors.New("resourceVersion changed"))
	})
	c.queue = workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
	t.Cleanup(c.queue.ShutDown)

	const key = "root:ws|cluster"
	err := c.process(context.Background(), key)
	require.NoError(t, err, "a conflict must not be returned as a hard error")

	require.Eventually(t, func() bool { return c.queue.Len() == 1 }, time.Second, 5*time.Millisecond,
		"the key should be requeued after a conflict")
}

type noopResolver struct{}

func (n *noopResolver) Resolve(*tenancyv1alpha1.WorkspaceType) ([]*tenancyv1alpha1.WorkspaceType, error) {
	return nil, nil
}
