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

package clusterworkspacetype

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
)

func TestReconcile(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		shards   []*corev1alpha1.Shard
		listErr  error
		cwts     []*tenancyv1alpha1.ClusterWorkspaceType
		getErr   error
		cwt      *tenancyv1alpha1.ClusterWorkspaceType
		expected *tenancyv1alpha1.ClusterWorkspaceType
	}{
		{
			name: "no shards, no URLs in status",
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "sometype",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "root:org:team:ws",
					},
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "sometype",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "root:org:team:ws",
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:   "Ready",
							Status: "True",
						},
						{
							Type:   "VirtualWorkspaceURLsReady",
							Status: "True",
						},
					},
				},
			},
		},
		{
			name:    "error listing shards, error in status",
			shards:  []*corev1alpha1.Shard{},
			listErr: fmt.Errorf("oops"),
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "sometype",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "root:org:team:ws",
					},
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "sometype",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "root:org:team:ws",
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:     "Ready",
							Status:   "False",
							Severity: "Error",
							Reason:   "ErrorGeneratingURLs",
							Message:  "error listing Shards: oops",
						},
						{
							Type:     "VirtualWorkspaceURLsReady",
							Status:   "False",
							Severity: "Error",
							Reason:   "ErrorGeneratingURLs",
							Message:  "error listing Shards: oops",
						},
					},
				},
			},
		},
		{
			name: "URLs from shards propagate fill empty status",
			shards: []*corev1alpha1.Shard{
				{Spec: corev1alpha1.ShardSpec{ExternalURL: "https://whatever.com"}},
				{Spec: corev1alpha1.ShardSpec{ExternalURL: "https://something.com"}},
				{Spec: corev1alpha1.ShardSpec{ExternalURL: "https://item.com"}},
			},
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "sometype",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "root:org:team:ws",
					},
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "sometype",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "root:org:team:ws",
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					VirtualWorkspaces: []tenancyv1alpha1.VirtualWorkspace{
						{URL: "https://item.com/services/initializingworkspaces/root:org:team:ws:sometype"},
						{URL: "https://something.com/services/initializingworkspaces/root:org:team:ws:sometype"},
						{URL: "https://whatever.com/services/initializingworkspaces/root:org:team:ws:sometype"},
					},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:   "Ready",
							Status: "True",
						},
						{
							Type:   "VirtualWorkspaceURLsReady",
							Status: "True",
						},
					},
				},
			},
		},
		{
			name: "URLs from shards propagate to partially filled status",
			shards: []*corev1alpha1.Shard{
				{Spec: corev1alpha1.ShardSpec{ExternalURL: "https://whatever.com"}},
				{Spec: corev1alpha1.ShardSpec{ExternalURL: "https://something.com"}},
				{Spec: corev1alpha1.ShardSpec{ExternalURL: "https://item.com"}},
			},
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "sometype",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "root:org:team:ws",
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					VirtualWorkspaces: []tenancyv1alpha1.VirtualWorkspace{
						{URL: "https://item.com/services/initializingworkspaces/root:org:team:ws:sometype"},
					},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:   "Ready",
							Status: "True",
						},
						{
							Type:   "VirtualWorkspaceURLsReady",
							Status: "True",
						},
					},
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "sometype",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "root:org:team:ws",
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					VirtualWorkspaces: []tenancyv1alpha1.VirtualWorkspace{
						{URL: "https://item.com/services/initializingworkspaces/root:org:team:ws:sometype"},
						{URL: "https://something.com/services/initializingworkspaces/root:org:team:ws:sometype"},
						{URL: "https://whatever.com/services/initializingworkspaces/root:org:team:ws:sometype"},
					},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:   "Ready",
							Status: "True",
						},
						{
							Type:   "VirtualWorkspaceURLsReady",
							Status: "True",
						},
					},
				},
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.cwts = append(testCase.cwts, testCase.cwt.DeepCopy())
			c := controller{
				listShards: func() ([]*corev1alpha1.Shard, error) {
					return testCase.shards, testCase.listErr
				},
				resolveClusterWorkspaceType: func(reference tenancyv1alpha1.ClusterWorkspaceTypeReference) (*tenancyv1alpha1.ClusterWorkspaceType, error) {
					if testCase.getErr != nil {
						return nil, testCase.getErr
					}
					for _, cwt := range testCase.cwts {
						if logicalcluster.From(cwt).String() == reference.Path && cwt.Name == string(reference.Name) {
							return cwt, nil
						}
					}
					return nil, errors.NewNotFound(tenancyv1alpha1.Resource("clusterworkspacetype"), string(reference.Name))
				},
			}
			c.reconcile(context.TODO(), testCase.cwt)
			c.reconcile(context.TODO(), testCase.cwt) // relationships require resolved extensions
			if diff := cmp.Diff(testCase.cwt, testCase.expected, cmpopts.IgnoreTypes(metav1.Time{})); diff != "" {
				t.Errorf("incorrect ClusterWorkspaceType after reconciliation: %v", diff)
			}
		})
	}
}
