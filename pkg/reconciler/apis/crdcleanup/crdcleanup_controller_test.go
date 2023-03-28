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

package crdcleanup

import (
	"context"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

func TestBoundCRDDeletion(t *testing.T) {
	schemaUID := "f1249438-5c68-11ed-823e-f875a46c726b"

	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test",
		},
		Status: apisv1alpha1.APIBindingStatus{
			Phase: apisv1alpha1.APIBindingPhaseBound,
			Conditions: conditionsv1alpha1.Conditions{{
				Type:   apisv1alpha1.BindingUpToDate,
				Status: corev1.ConditionTrue,
			}},
			BoundResources: []apisv1alpha1.BoundAPIResource{{
				Schema: apisv1alpha1.BoundAPIResourceSchema{
					UID: schemaUID,
				},
			}},
		},
	}

	oldEnoughToDelete := time.Now().Add((AgeThreshold * -1) - time.Second)

	tests := []struct {
		name                       string
		creationTimestamp          time.Time
		hasBindings                bool
		expectDeletion             bool
		expectRequeue              bool
		hasBindingsAfterRequeue    bool
		expectDeletionAfterRequeue bool
	}{
		{
			name:              "find matching binding in index (no requeue)",
			creationTimestamp: oldEnoughToDelete,
			hasBindings:       true,
			expectDeletion:    false,
			expectRequeue:     false,
		},
		{
			name:              "no bindings (no requeue)",
			creationTimestamp: oldEnoughToDelete,
			hasBindings:       false,
			expectDeletion:    true,
			expectRequeue:     false,
		},
		{
			name:                       "CRD won't have bindings after requeue",
			creationTimestamp:          time.Now(),
			hasBindings:                false,
			expectDeletion:             false,
			expectRequeue:              true,
			hasBindingsAfterRequeue:    false,
			expectDeletionAfterRequeue: true,
		},
		{
			name:                       "CRD will have bindings after requeue",
			creationTimestamp:          time.Now(),
			hasBindings:                false,
			expectDeletion:             false,
			expectRequeue:              true,
			hasBindingsAfterRequeue:    true,
			expectDeletionAfterRequeue: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deleteHappened := false

			crd := &apiextensionsv1.CustomResourceDefinition{}
			crd.SetName(schemaUID)

			q := testRateLimitingQueue{
				workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),
				false,
			}

			controller := &controller{
				queue: &q,
				getCRD: func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
					return crd, nil
				},
				getAPIBindingsByBoundResourceUID: func(name string) ([]*apisv1alpha1.APIBinding, error) {
					if !q.requeueHappened && tt.hasBindings {
						return []*apisv1alpha1.APIBinding{apiBinding}, nil
					} else if q.requeueHappened && tt.hasBindingsAfterRequeue {
						return []*apisv1alpha1.APIBinding{apiBinding}, nil
					}
					return []*apisv1alpha1.APIBinding{}, nil
				},
				deleteCRD: func(ctx context.Context, name string) error {
					deleteHappened = true
					return nil
				},
			}

			testController := func(creationTimestamp time.Time, expectDeletion bool) {
				crd.ObjectMeta.CreationTimestamp = metav1.NewTime(creationTimestamp)
				err := controller.process(context.Background(), schemaUID)
				if err != nil {
					t.Errorf("Unexpected error: %q", err)
				}

				if deleteHappened != expectDeletion {
					t.Errorf("Expected deletion: %t, but instead actual deletion: %t", expectDeletion, deleteHappened)
				}
			}

			testController(tt.creationTimestamp, tt.expectDeletion)

			if tt.expectRequeue != q.requeueHappened {
				t.Errorf("Expected requeue: %t, but instead acutal requeue: %t", tt.expectRequeue, q.requeueHappened)
			}

			if tt.expectRequeue {
				// Test time passing but not long enough to trigger a delete
				// This is to ensure CRDs do not get deleted before the configured threshold
				testController(tt.creationTimestamp.Add(((AgeThreshold/2)+time.Second)*-1), false)

				// This should be enough time passing to trigger a delete (if expected)
				testController(tt.creationTimestamp.Add((AgeThreshold+time.Second)*-1), tt.expectDeletionAfterRequeue)
			}
		})
	}
}

type testRateLimitingQueue struct {
	workqueue.RateLimitingInterface
	requeueHappened bool
}

func (q *testRateLimitingQueue) AddAfter(item interface{}, duration time.Duration) {
	q.requeueHappened = true
}
