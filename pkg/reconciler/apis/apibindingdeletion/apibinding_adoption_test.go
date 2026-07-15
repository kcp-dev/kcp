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

package apibindingdeletion

import (
	"context"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

const (
	testCluster      = "root:consumer"
	providerPath     = "root:provider"
	cowboySchemaName = "v1.cowboys.wildwest.dev"
	cowboySchemaUID  = "cowboy-schema-uid"
	identity         = "abc123"
)

func deletingBinding(policy apisv1alpha2.APIBindingDeletionPolicy) *apisv1alpha2.APIBinding {
	now := metav1.Now()
	return &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "wildwest",
			DeletionTimestamp: &now,
			Finalizers:        []string{APIBindingFinalizer},
			Annotations:       map[string]string{logicalcluster.AnnotationKey: testCluster},
		},
		Spec: apisv1alpha2.APIBindingSpec{
			DeletionPolicy: policy,
		},
		Status: apisv1alpha2.APIBindingStatus{
			BoundResources: []apisv1alpha2.BoundAPIResource{
				{
					Group:           "wildwest.dev",
					Resource:        "cowboys",
					StorageVersions: []string{"v1alpha1"},
					Schema: apisv1alpha2.BoundAPIResourceSchema{
						Name:         cowboySchemaName,
						UID:          cowboySchemaUID,
						IdentityHash: identity,
					},
				},
			},
		},
	}
}

func candidateBinding(name, exportName string) *apisv1alpha2.APIBinding {
	return &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{logicalcluster.AnnotationKey: testCluster},
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath,
					Name: exportName,
				},
			},
		},
	}
}

func cowboysExport(name, identityHash, schemaName string) *apisv1alpha2.APIExport {
	return &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{logicalcluster.AnnotationKey: providerPath},
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Group:  "wildwest.dev",
					Name:   "cowboys",
					Schema: schemaName,
				},
			},
		},
		Status: apisv1alpha2.APIExportStatus{
			IdentityHash: identityHash,
		},
	}
}

func liveLogicalCluster() *corev1alpha1.LogicalCluster {
	return &corev1alpha1.LogicalCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:        corev1alpha1.LogicalClusterName,
			Annotations: map[string]string{logicalcluster.AnnotationKey: testCluster},
		},
	}
}

func TestRetainedResources(t *testing.T) {
	t.Parallel()

	cowboysGR := schema.GroupResource{Group: "wildwest.dev", Resource: "cowboys"}

	tests := []struct {
		name           string
		policy         apisv1alpha2.APIBindingDeletionPolicy
		bindings       []*apisv1alpha2.APIBinding
		exports        map[string]*apisv1alpha2.APIExport
		schemas        map[string]*apisv1alpha1.APIResourceSchema
		logicalCluster *corev1alpha1.LogicalCluster

		expectRetained  bool
		expectReason    retentionReason
		expectSuccessor string
	}{
		{
			name:           "no successor, Delete policy: not retained",
			policy:         apisv1alpha2.APIBindingDeletionPolicyDelete,
			expectRetained: false,
		},
		{
			name:           "no successor, WaitForSuccessor policy: retained, waiting",
			policy:         apisv1alpha2.APIBindingDeletionPolicyWaitForSuccessor,
			expectRetained: true,
			expectReason:   retainedWaiting,
		},
		{
			name:   "WaitForSuccessor degrades to Delete when the LogicalCluster is deleting",
			policy: apisv1alpha2.APIBindingDeletionPolicyWaitForSuccessor,
			logicalCluster: func() *corev1alpha1.LogicalCluster {
				lc := liveLogicalCluster()
				now := metav1.Now()
				lc.DeletionTimestamp = &now
				return lc
			}(),
			expectRetained: false,
		},
		{
			name:     "successor with same schema UID and identity: adopted regardless of policy",
			policy:   apisv1alpha2.APIBindingDeletionPolicyDelete,
			bindings: []*apisv1alpha2.APIBinding{candidateBinding("cowboys", "cowboys")},
			exports: map[string]*apisv1alpha2.APIExport{
				"cowboys": cowboysExport("cowboys", identity, cowboySchemaName),
			},
			schemas: map[string]*apisv1alpha1.APIResourceSchema{
				cowboySchemaName: {ObjectMeta: metav1.ObjectMeta{Name: cowboySchemaName, UID: types.UID(cowboySchemaUID)}},
			},
			expectRetained:  true,
			expectReason:    retainedAdopted,
			expectSuccessor: "cowboys",
		},
		{
			name:     "successor export has different identity: not adopted",
			policy:   apisv1alpha2.APIBindingDeletionPolicyDelete,
			bindings: []*apisv1alpha2.APIBinding{candidateBinding("cowboys", "cowboys")},
			exports: map[string]*apisv1alpha2.APIExport{
				"cowboys": cowboysExport("cowboys", "other-identity", cowboySchemaName),
			},
			schemas: map[string]*apisv1alpha1.APIResourceSchema{
				cowboySchemaName: {ObjectMeta: metav1.ObjectMeta{Name: cowboySchemaName, UID: types.UID(cowboySchemaUID)}},
			},
			expectRetained: false,
		},
		{
			name:     "successor schema has different UID: not adopted",
			policy:   apisv1alpha2.APIBindingDeletionPolicyDelete,
			bindings: []*apisv1alpha2.APIBinding{candidateBinding("cowboys", "cowboys")},
			exports: map[string]*apisv1alpha2.APIExport{
				"cowboys": cowboysExport("cowboys", identity, cowboySchemaName),
			},
			schemas: map[string]*apisv1alpha1.APIResourceSchema{
				cowboySchemaName: {ObjectMeta: metav1.ObjectMeta{Name: cowboySchemaName, UID: types.UID("other-uid")}},
			},
			expectRetained: false,
		},
		{
			name:   "successor being deleted itself: ignored",
			policy: apisv1alpha2.APIBindingDeletionPolicyDelete,
			bindings: func() []*apisv1alpha2.APIBinding {
				b := candidateBinding("cowboys", "cowboys")
				now := metav1.Now()
				b.DeletionTimestamp = &now
				return []*apisv1alpha2.APIBinding{b}
			}(),
			exports: map[string]*apisv1alpha2.APIExport{
				"cowboys": cowboysExport("cowboys", identity, cowboySchemaName),
			},
			schemas: map[string]*apisv1alpha1.APIResourceSchema{
				cowboySchemaName: {ObjectMeta: metav1.ObjectMeta{Name: cowboySchemaName, UID: types.UID(cowboySchemaUID)}},
			},
			expectRetained: false,
		},
		{
			name:           "successor export unresolvable: not adopted, WaitForSuccessor keeps waiting",
			policy:         apisv1alpha2.APIBindingDeletionPolicyWaitForSuccessor,
			bindings:       []*apisv1alpha2.APIBinding{candidateBinding("cowboys", "cowboys")},
			expectRetained: true,
			expectReason:   retainedWaiting,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			deleting := deletingBinding(tt.policy)
			lc := tt.logicalCluster
			if lc == nil {
				lc = liveLogicalCluster()
			}
			c := &Controller{
				listAPIBindings: func(cluster logicalcluster.Name) ([]*apisv1alpha2.APIBinding, error) {
					return append([]*apisv1alpha2.APIBinding{deleting}, tt.bindings...), nil
				},
				getAPIExportByPath: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
					if export, ok := tt.exports[name]; ok {
						return export, nil
					}
					return nil, fmt.Errorf("APIExport %s|%s not found", path, name)
				},
				getAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					if sch, ok := tt.schemas[name]; ok {
						return sch, nil
					}
					return nil, fmt.Errorf("APIResourceSchema %s|%s not found", cluster, name)
				},
				getLogicalCluster: func(cluster logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
					return lc, nil
				},
			}

			retained, err := c.retainedResources(logicalcluster.Name(testCluster), deleting)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			r, ok := retained[cowboysGR]
			if ok != tt.expectRetained {
				t.Fatalf("expected retained=%v, got %v (%v)", tt.expectRetained, ok, retained)
			}
			if !tt.expectRetained {
				return
			}
			if r.reason != tt.expectReason {
				t.Errorf("expected reason %q, got %q", tt.expectReason, r.reason)
			}
			if r.successor != tt.expectSuccessor {
				t.Errorf("expected successor %q, got %q", tt.expectSuccessor, r.successor)
			}
		})
	}
}

func TestDeleteAllCRsSkipsAdopted(t *testing.T) {
	t.Parallel()

	deleting := deletingBinding(apisv1alpha2.APIBindingDeletionPolicyDelete)

	deleted := map[schema.GroupVersionResource]bool{}
	c := &Controller{
		listAPIBindings: func(cluster logicalcluster.Name) ([]*apisv1alpha2.APIBinding, error) {
			return []*apisv1alpha2.APIBinding{deleting, candidateBinding("cowboys", "cowboys")}, nil
		},
		getAPIExportByPath: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			return cowboysExport("cowboys", identity, cowboySchemaName), nil
		},
		getAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
			return &apisv1alpha1.APIResourceSchema{ObjectMeta: metav1.ObjectMeta{Name: cowboySchemaName, UID: types.UID(cowboySchemaUID)}}, nil
		},
		getLogicalCluster: func(cluster logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
			return liveLogicalCluster(), nil
		},
		listResources: func(ctx context.Context, cluster logicalcluster.Path, gvr schema.GroupVersionResource) (*metav1.PartialObjectMetadataList, error) {
			return &metav1.PartialObjectMetadataList{Items: []metav1.PartialObjectMetadata{
				*newPartialObject("wildwest.dev/v1alpha1", "Cowboy", "luke", "saloon", nil),
			}}, nil
		},
		deleteResources: func(ctx context.Context, cluster logicalcluster.Path, gvr schema.GroupVersionResource, namespace string) error {
			deleted[gvr] = true
			return nil
		},
	}

	remaining, err := c.deleteAllCRs(context.Background(), deleting)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deleted) != 0 {
		t.Errorf("expected no deletions for adopted resources, got %v", deleted)
	}
	if len(remaining.gvrToNumRemaining) != 0 {
		t.Errorf("adopted resources must not count as remaining (would block finalization), got %v", remaining.gvrToNumRemaining)
	}
	if remaining.gvrsWaitingForSuccessor.Len() != 0 {
		t.Errorf("adopted resources must not be marked waiting, got %v", remaining.gvrsWaitingForSuccessor)
	}
}

func TestDeleteAllCRsWaitsForSuccessor(t *testing.T) {
	t.Parallel()

	deleting := deletingBinding(apisv1alpha2.APIBindingDeletionPolicyWaitForSuccessor)
	cowboysGVR := schema.GroupVersionResource{Group: "wildwest.dev", Resource: "cowboys", Version: "v1alpha1"}

	deleted := map[schema.GroupVersionResource]bool{}
	c := &Controller{
		listAPIBindings: func(cluster logicalcluster.Name) ([]*apisv1alpha2.APIBinding, error) {
			return []*apisv1alpha2.APIBinding{deleting}, nil
		},
		getAPIExportByPath: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			return nil, fmt.Errorf("no exports")
		},
		getAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
			return nil, fmt.Errorf("no schemas")
		},
		getLogicalCluster: func(cluster logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
			return liveLogicalCluster(), nil
		},
		listResources: func(ctx context.Context, cluster logicalcluster.Path, gvr schema.GroupVersionResource) (*metav1.PartialObjectMetadataList, error) {
			return &metav1.PartialObjectMetadataList{Items: []metav1.PartialObjectMetadata{
				*newPartialObject("wildwest.dev/v1alpha1", "Cowboy", "luke", "saloon", nil),
			}}, nil
		},
		deleteResources: func(ctx context.Context, cluster logicalcluster.Path, gvr schema.GroupVersionResource, namespace string) error {
			deleted[gvr] = true
			return nil
		},
	}

	remaining, err := c.deleteAllCRs(context.Background(), deleting)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deleted) != 0 {
		t.Errorf("expected no deletions under WaitForSuccessor, got %v", deleted)
	}
	if got := remaining.gvrToNumRemaining[cowboysGVR]; got != 1 {
		t.Errorf("expected 1 remaining instance for %v (blocks finalization), got %d", cowboysGVR, got)
	}
	if !remaining.gvrsWaitingForSuccessor.Has(cowboysGVR) {
		t.Errorf("expected %v to be marked waiting for successor, got %v", cowboysGVR, remaining.gvrsWaitingForSuccessor)
	}

	// the status mutation must hold the finalizer with the WaitingForSuccessor reason
	mutated, mutateErr := c.mutateResourceRemainingStatus(remaining, deleting.DeepCopy())
	if mutateErr == nil {
		t.Fatalf("expected a ResourcesRemainingError to keep the finalizer held")
	}
	cond := conditions.Get(mutated, apisv1alpha2.BindingResourceDeleteSuccess)
	if cond == nil {
		t.Fatalf("missing condition %s", apisv1alpha2.BindingResourceDeleteSuccess)
	}
	if cond.Reason != WaitingForSuccessorReason {
		t.Errorf("expected condition reason %q, got %q", WaitingForSuccessorReason, cond.Reason)
	}
}
