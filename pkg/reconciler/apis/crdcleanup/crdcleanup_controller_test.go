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

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
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

	establishedCRD := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:              schemaUID,
			CreationTimestamp: metav1.NewTime(time.Now().Add(time.Second * -11)),
		},
		Status: apiextensionsv1.CustomResourceDefinitionStatus{
			Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{{
				Type:   apiextensionsv1.Established,
				Status: apiextensionsv1.ConditionTrue,
			}},
		},
	}

	newCRD := establishedCRD.DeepCopy()
	newCRD.Status.Conditions[0].Status = apiextensionsv1.ConditionFalse
	newCRD.CreationTimestamp = metav1.NewTime(time.Now())

	tests := []struct {
		name               string
		crd                *apiextensionsv1.CustomResourceDefinition
		apiBindingsInIndex []*apisv1alpha1.APIBinding
		expectDeletion     bool
	}{
		{
			name:               "find matching binding in index",
			crd:                establishedCRD,
			apiBindingsInIndex: []*apisv1alpha1.APIBinding{apiBinding},
			expectDeletion:     false,
		},
		{
			name:               "no bindings",
			crd:                establishedCRD,
			apiBindingsInIndex: []*apisv1alpha1.APIBinding{},
			expectDeletion:     true,
		},
		{
			name:               "CRD is not old enough to delete",
			crd:                newCRD,
			apiBindingsInIndex: []*apisv1alpha1.APIBinding{},
			expectDeletion:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deleteHappened := false

			controller := &controller{
				queue: workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),
				getCRD: func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
					return tt.crd, nil
				},
				getAPIBindingsByBoundResourceUID: func(name string) ([]*apisv1alpha1.APIBinding, error) {
					return tt.apiBindingsInIndex, nil
				},
				deleteCRD: func(ctx context.Context, name string) error {
					deleteHappened = true
					return nil
				},
			}

			err := controller.process(context.Background(), schemaUID)
			if err != nil {
				t.Errorf("Unexpected error: %q", err)
			}

			if deleteHappened != tt.expectDeletion {
				t.Errorf("Expected deletion: %t, but instead actual deletion: %t", tt.expectDeletion, deleteHappened)
			}
		})
	}
}
