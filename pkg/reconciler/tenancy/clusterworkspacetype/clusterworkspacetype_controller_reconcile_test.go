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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
)

func TestReconcile(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		shards   []*tenancyv1alpha1.ClusterWorkspaceShard
		listErr  error
		cwt      *tenancyv1alpha1.ClusterWorkspaceType
		expected *tenancyv1alpha1.ClusterWorkspaceType
	}{
		{
			name: "no shards, no URLs in status",
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					Conditions: conditionsv1alpha1.Conditions{
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
			shards:  []*tenancyv1alpha1.ClusterWorkspaceShard{},
			listErr: fmt.Errorf("oops"),
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:     "VirtualWorkspaceURLsReady",
							Status:   "False",
							Severity: "Error",
							Reason:   "ErrorGeneratingURLs",
							Message:  "error listing ClusterWorkspaceShards: oops",
						},
					},
				},
			},
		},
		{
			name: "URLs from shards propagate fill empty status",
			shards: []*tenancyv1alpha1.ClusterWorkspaceShard{
				{Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{ExternalURL: "https://whatever.com"}},
				{Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{ExternalURL: "https://something.com"}},
				{Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{ExternalURL: "https://item.com"}},
			},
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					VirtualWorkspaces: []tenancyv1alpha1.VirtualWorkspace{
						{URL: "https://item.com/services/initializingworkspaces/root:org:team:ws:SomeType"},
						{URL: "https://something.com/services/initializingworkspaces/root:org:team:ws:SomeType"},
						{URL: "https://whatever.com/services/initializingworkspaces/root:org:team:ws:SomeType"},
					},
					Conditions: conditionsv1alpha1.Conditions{
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
			shards: []*tenancyv1alpha1.ClusterWorkspaceShard{
				{Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{ExternalURL: "https://whatever.com"}},
				{Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{ExternalURL: "https://something.com"}},
				{Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{ExternalURL: "https://item.com"}},
			},
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					VirtualWorkspaces: []tenancyv1alpha1.VirtualWorkspace{
						{URL: "https://item.com/services/initializingworkspaces/root:org:team:ws:SomeType"},
					},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:   "VirtualWorkspaceURLsReady",
							Status: "True",
						},
					},
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					VirtualWorkspaces: []tenancyv1alpha1.VirtualWorkspace{
						{URL: "https://item.com/services/initializingworkspaces/root:org:team:ws:SomeType"},
						{URL: "https://something.com/services/initializingworkspaces/root:org:team:ws:SomeType"},
						{URL: "https://whatever.com/services/initializingworkspaces/root:org:team:ws:SomeType"},
					},
					Conditions: conditionsv1alpha1.Conditions{
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
			c := controller{
				listClusterWorkspaceShards: func() ([]*tenancyv1alpha1.ClusterWorkspaceShard, error) {
					return testCase.shards, testCase.listErr
				},
			}
			c.reconcile(context.TODO(), testCase.cwt)
			if diff := cmp.Diff(testCase.cwt, testCase.expected, cmpopts.IgnoreTypes(metav1.Time{})); diff != "" {
				t.Errorf("incorrect ClusterWorkspaceType after reconciliation: %v", diff)
			}
		})
	}
}
