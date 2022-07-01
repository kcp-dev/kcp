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

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
)

func TestReconcile(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		shards   []*tenancyv1alpha1.ClusterWorkspaceShard
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
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Path: "root:org:team:ws", Name: "SomeType"},
					},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:   "Ready",
							Status: "True",
						},
						{
							Type:   "ExtensionsResolved",
							Status: "True",
						},
						{
							Type:   "RelationshipsValid",
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
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Path: "root:org:team:ws", Name: "SomeType"},
					},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:     "Ready",
							Status:   "False",
							Severity: "Error",
							Reason:   "ErrorGeneratingURLs",
							Message:  "error listing ClusterWorkspaceShards: oops",
						},
						{
							Type:   "ExtensionsResolved",
							Status: "True",
						},
						{
							Type:   "RelationshipsValid",
							Status: "True",
						},
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
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Path: "root:org:team:ws", Name: "SomeType"},
					},
					VirtualWorkspaces: []tenancyv1alpha1.VirtualWorkspace{
						{URL: "https://item.com/services/initializingworkspaces/root:org:team:ws:SomeType"},
						{URL: "https://something.com/services/initializingworkspaces/root:org:team:ws:SomeType"},
						{URL: "https://whatever.com/services/initializingworkspaces/root:org:team:ws:SomeType"},
					},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:   "Ready",
							Status: "True",
						},
						{
							Type:   "ExtensionsResolved",
							Status: "True",
						},
						{
							Type:   "RelationshipsValid",
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
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Path: "root:org:team:ws", Name: "SomeType"},
					},
					VirtualWorkspaces: []tenancyv1alpha1.VirtualWorkspace{
						{URL: "https://item.com/services/initializingworkspaces/root:org:team:ws:SomeType"},
					},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:   "Ready",
							Status: "True",
						},
						{
							Type:   "ExtensionsResolved",
							Status: "True",
						},
						{
							Type:   "RelationshipsValid",
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
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Path: "root:org:team:ws", Name: "SomeType"},
					},
					VirtualWorkspaces: []tenancyv1alpha1.VirtualWorkspace{
						{URL: "https://item.com/services/initializingworkspaces/root:org:team:ws:SomeType"},
						{URL: "https://something.com/services/initializingworkspaces/root:org:team:ws:SomeType"},
						{URL: "https://whatever.com/services/initializingworkspaces/root:org:team:ws:SomeType"},
					},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:   "Ready",
							Status: "True",
						},
						{
							Type:   "ExtensionsResolved",
							Status: "True",
						},
						{
							Type:   "RelationshipsValid",
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
			name: "no type extensions, no self initializer, no initializers in status",
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
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Path: "root:org:team:ws", Name: "SomeType"},
					},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:   "Ready",
							Status: "True",
						},
						{
							Type:   "ExtensionsResolved",
							Status: "True",
						},
						{
							Type:   "RelationshipsValid",
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
			name: "no type extensions, self initializer, only self initializer in status",
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Initializer: true,
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Initializer: true,
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Path: "root:org:team:ws", Name: "SomeType"},
					},
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{
						"root:org:team:ws:SomeType",
					},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:   "Ready",
							Status: "True",
						},
						{
							Type:   "ExtensionsResolved",
							Status: "True",
						},
						{
							Type:   "RelationshipsValid",
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
			name: "simple type extension by union, brings in initializers where possible and aliases both types",
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Initializer: true,
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "First", Path: "root:org:team:ws"},
							{Name: "Second", Path: "root:org:team:ws"},
						},
					},
				},
			},
			cwts: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "first",
						ClusterName: "root:org:team:ws",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializer: true,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "second",
						ClusterName: "root:org:team:ws",
					},
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Initializer: true,
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "First", Path: "root:org:team:ws"},
							{Name: "Second", Path: "root:org:team:ws"},
						},
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{
						"root:org:team:ws:First",
						"root:org:team:ws:SomeType",
					},
					TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "First", Path: "root:org:team:ws"},
						{Name: "Second", Path: "root:org:team:ws"},
						{Name: "SomeType", Path: "root:org:team:ws"},
					},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:   "Ready",
							Status: "True",
						},
						{
							Type:   "ExtensionsResolved",
							Status: "True",
						},
						{
							Type:   "RelationshipsValid",
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
			name: "type extension using without removes something that a parent adds",
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Initializer: true,
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "First", Path: "root:org:team:ws"},
						},
						Without: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "Second", Path: "root:org:team:ws"},
						},
					},
				},
			},
			cwts: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "first",
						ClusterName: "root:org:team:ws",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializer: true,
						Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
							With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
								{Name: "Second", Path: "root:org:team:ws"},
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "second",
						ClusterName: "root:org:team:ws",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializer: true,
					},
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Initializer: true,
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "First", Path: "root:org:team:ws"},
						},
						Without: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "Second", Path: "root:org:team:ws"},
						},
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{
						"root:org:team:ws:First",
						"root:org:team:ws:SomeType",
					},
					TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "SomeType", Path: "root:org:team:ws"},
					},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:   "Ready",
							Status: "True",
						},
						{
							Type:   "ExtensionsResolved",
							Status: "True",
						},
						{
							Type:   "RelationshipsValid",
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
			name: "type extension on nonexistent type",
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Initializer: true,
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "First", Path: "root:org:team:ws"},
						},
					},
				},
			},
			cwts: []*tenancyv1alpha1.ClusterWorkspaceType{},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Initializer: true,
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "First", Path: "root:org:team:ws"},
						},
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:     "Ready",
							Status:   "False",
							Severity: "Error",
							Reason:   "ErrorResolvingExtensions",
							Message:  `clusterworkspacetype.tenancy.kcp.dev "First" not found`,
						},
						{
							Type:     "ExtensionsResolved",
							Status:   "False",
							Severity: "Error",
							Reason:   "ErrorResolvingExtensions",
							Message:  `clusterworkspacetype.tenancy.kcp.dev "First" not found`,
						},
						{
							Type:     "RelationshipsValid",
							Status:   "False",
							Severity: "Error",
							Reason:   "ErrorValidatingRelationships",
							Message:  "type extensions have not yet been resolved",
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
			name: "type extension uses a self-reference",
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Initializer: true,
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "SomeType", Path: "root:org:team:ws"},
						},
					},
				},
			},
			cwts: []*tenancyv1alpha1.ClusterWorkspaceType{},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Initializer: true,
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "SomeType", Path: "root:org:team:ws"},
						},
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:     "Ready",
							Status:   "False",
							Severity: "Error",
							Reason:   "ErrorResolvingExtensions",
							Message:  `cannot use a self-reference during type extension`,
						},
						{
							Type:     "ExtensionsResolved",
							Status:   "False",
							Severity: "Error",
							Reason:   "ErrorResolvingExtensions",
							Message:  `cannot use a self-reference during type extension`,
						},
						{
							Type:     "RelationshipsValid",
							Status:   "False",
							Severity: "Error",
							Reason:   "ErrorValidatingRelationships",
							Message:  "type extensions have not yet been resolved",
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
			name: "type extension causes a cycle",
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Initializer: true,
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "OtherType", Path: "root:org:team:ws"},
						},
					},
				},
			},
			cwts: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "OtherType",
						ClusterName: "root:org:team:ws",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializer: true,
						Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
							With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
								{Name: "SomeType", Path: "root:org:team:ws"},
							},
						},
					},
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Initializer: true,
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "OtherType", Path: "root:org:team:ws"},
						},
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:     "Ready",
							Status:   "False",
							Severity: "Error",
							Reason:   "ErrorResolvingExtensions",
							Message:  `type extension creates a cycle: [root:org:team:ws:SomeType, root:org:team:ws:OtherType, root:org:team:ws:SomeType]`,
						},
						{
							Type:     "ExtensionsResolved",
							Status:   "False",
							Severity: "Error",
							Reason:   "ErrorResolvingExtensions",
							Message:  `type extension creates a cycle: [root:org:team:ws:SomeType, root:org:team:ws:OtherType, root:org:team:ws:SomeType]`,
						},
						{
							Type:     "RelationshipsValid",
							Status:   "False",
							Severity: "Error",
							Reason:   "ErrorValidatingRelationships",
							Message:  "type extensions have not yet been resolved",
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
			name: "type extension deduplicates parent types",
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "First", Path: "root:org:team:ws"},
							{Name: "Second", Path: "root:org:team:ws"},
						},
					},
				},
			},
			cwts: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "first",
						ClusterName: "root:org:team:ws",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
							With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
								{Name: "Third", Path: "root:org:team:ws"},
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "second",
						ClusterName: "root:org:team:ws",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
							With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
								{Name: "Third", Path: "root:org:team:ws"},
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "third",
						ClusterName: "root:org:team:ws",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializer: true,
					},
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "First", Path: "root:org:team:ws"},
							{Name: "Second", Path: "root:org:team:ws"},
						},
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{
						"root:org:team:ws:Third",
					},
					TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "First", Path: "root:org:team:ws"},
						{Name: "Second", Path: "root:org:team:ws"},
						{Name: "SomeType", Path: "root:org:team:ws"},
						{Name: "Third", Path: "root:org:team:ws"},
					},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:   "Ready",
							Status: "True",
						},
						{
							Type:   "ExtensionsResolved",
							Status: "True",
						},
						{
							Type:   "RelationshipsValid",
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
			name: "type extension without is resolved at each node, so you can re-add something that is removed higher up",
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "Second", Path: "root:org:team:ws"},
							{Name: "Third", Path: "root:org:team:ws"}, // Second removes this, but we re-add it
						},
					},
				},
			},
			cwts: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "first",
						ClusterName: "root:org:team:ws",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
							With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
								{Name: "Third", Path: "root:org:team:ws"},
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "second",
						ClusterName: "root:org:team:ws",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
							With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
								{Name: "First", Path: "root:org:team:ws"},
							},
							Without: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
								{Name: "Third", Path: "root:org:team:ws"},
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "third",
						ClusterName: "root:org:team:ws",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializer: true,
					},
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "Second", Path: "root:org:team:ws"},
							{Name: "Third", Path: "root:org:team:ws"},
						},
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{
						"root:org:team:ws:Third",
					},
					TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "Second", Path: "root:org:team:ws"},
						{Name: "SomeType", Path: "root:org:team:ws"},
						{Name: "Third", Path: "root:org:team:ws"},
					},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:   "Ready",
							Status: "True",
						},
						{
							Type:   "ExtensionsResolved",
							Status: "True",
						},
						{
							Type:   "RelationshipsValid",
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
			name: "type extension without removing part of a composed parent type means you are not aliasing the parent",
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "First", Path: "root:org:team:ws"},
						},
						Without: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "Second", Path: "root:org:team:ws"},
						},
					},
				},
			},
			cwts: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "first",
						ClusterName: "root:org:team:ws",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
							With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
								{Name: "Second", Path: "root:org:team:ws"},
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "second",
						ClusterName: "root:org:team:ws",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializer: true,
					},
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "First", Path: "root:org:team:ws"},
						},
						Without: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "Second", Path: "root:org:team:ws"},
						},
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "SomeType", Path: "root:org:team:ws"},
					},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:   "Ready",
							Status: "True",
						},
						{
							Type:   "ExtensionsResolved",
							Status: "True",
						},
						{
							Type:   "RelationshipsValid",
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
			name: "type extension causes multiple cycles",
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Initializer: true,
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "OtherType", Path: "root:org:team:ws"},
							{Name: "DifferentType", Path: "root:org:team:ws"},
						},
					},
				},
			},
			cwts: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "OtherType",
						ClusterName: "root:org:team:ws",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializer: true,
						Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
							With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
								{Name: "SomeType", Path: "root:org:team:ws"},
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "DifferentType",
						ClusterName: "root:org:team:ws",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializer: true,
						Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
							With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
								{Name: "SomeType", Path: "root:org:team:ws"},
							},
						},
					},
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Initializer: true,
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "OtherType", Path: "root:org:team:ws"},
							{Name: "DifferentType", Path: "root:org:team:ws"},
						},
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:     "Ready",
							Status:   "False",
							Severity: "Error",
							Reason:   "ErrorResolvingExtensions",
							Message:  `type extension creates cycles: [root:org:team:ws:SomeType, root:org:team:ws:DifferentType, root:org:team:ws:SomeType], [root:org:team:ws:SomeType, root:org:team:ws:OtherType, root:org:team:ws:SomeType]`,
						},
						{
							Type:     "ExtensionsResolved",
							Status:   "False",
							Severity: "Error",
							Reason:   "ErrorResolvingExtensions",
							Message:  `type extension creates cycles: [root:org:team:ws:SomeType, root:org:team:ws:DifferentType, root:org:team:ws:SomeType], [root:org:team:ws:SomeType, root:org:team:ws:OtherType, root:org:team:ws:SomeType]`,
						},
						{
							Type:     "RelationshipsValid",
							Status:   "False",
							Severity: "Error",
							Reason:   "ErrorValidatingRelationships",
							Message:  "type extensions have not yet been resolved",
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
			name: "not including default type in allowed children is an error",
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					DefaultChildWorkspaceType: tenancyv1alpha1.ClusterWorkspaceTypeReference{Name: "OtherType", Path: "root:org:team:ws"},
				},
			},
			cwts: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "OtherType",
						ClusterName: "root:org:team:ws",
					},
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					DefaultChildWorkspaceType: tenancyv1alpha1.ClusterWorkspaceTypeReference{Name: "OtherType", Path: "root:org:team:ws"},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					TypeAliases:  []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "SomeType", Path: "root:org:team:ws"}},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:     "Ready",
							Status:   "False",
							Severity: "Error",
							Reason:   "ErrorValidatingRelationships",
							Message:  "default child type root:org:team:ws:OtherType is not allowed as a child",
						},
						{
							Type:   "ExtensionsResolved",
							Status: "True",
						},
						{
							Type:     "RelationshipsValid",
							Status:   "False",
							Severity: "Error",
							Reason:   "ErrorValidatingRelationships",
							Message:  "default child type root:org:team:ws:OtherType is not allowed as a child",
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
			name: "child type does not allow us as a parent",
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "OtherType", Path: "root:org:team:ws"},
					},
				},
			},
			cwts: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "OtherType",
						ClusterName: "root:org:team:ws",
					},
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "OtherType", Path: "root:org:team:ws"},
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					TypeAliases:  []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "SomeType", Path: "root:org:team:ws"}},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:     "Ready",
							Status:   "False",
							Severity: "Error",
							Reason:   "ErrorValidatingRelationships",
							Message:  "child root:org:team:ws:OtherType does not allow root:org:team:ws:SomeType as a parent",
						},
						{
							Type:   "ExtensionsResolved",
							Status: "True",
						},
						{
							Type:     "RelationshipsValid",
							Status:   "False",
							Severity: "Error",
							Reason:   "ErrorValidatingRelationships",
							Message:  "child root:org:team:ws:OtherType does not allow root:org:team:ws:SomeType as a parent",
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
			name: "child type allows anything as a parent",
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "OtherType", Path: "root:org:team:ws"},
					},
				},
			},
			cwts: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "OtherType",
						ClusterName: "root:org:team:ws",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							tenancyv1alpha1.AnyWorkspaceTypeReference,
						},
					},
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "OtherType", Path: "root:org:team:ws"},
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					TypeAliases:  []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "SomeType", Path: "root:org:team:ws"}},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:   "Ready",
							Status: "True",
						},
						{
							Type:   "ExtensionsResolved",
							Status: "True",
						},
						{
							Type:   "RelationshipsValid",
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
			name: "child type explicitly allows us as a parent",
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "OtherType", Path: "root:org:team:ws"},
					},
				},
			},
			cwts: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "OtherType",
						ClusterName: "root:org:team:ws",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "SomeType", Path: "root:org:team:ws"},
						},
					},
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "OtherType", Path: "root:org:team:ws"},
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					TypeAliases:  []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "SomeType", Path: "root:org:team:ws"}},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:   "Ready",
							Status: "True",
						},
						{
							Type:   "ExtensionsResolved",
							Status: "True",
						},
						{
							Type:   "RelationshipsValid",
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
			name: "child type implicitly allows us as a parent through an alias",
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "OtherType", Path: "root:org:team:ws"},
						},
					},
					AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "OtherType", Path: "root:org:team:ws"},
					},
				},
			},
			cwts: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "OtherType",
						ClusterName: "root:org:team:ws",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "OtherType", Path: "root:org:team:ws"},
						},
					},
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "OtherType", Path: "root:org:team:ws"},
						},
					},
					AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "OtherType", Path: "root:org:team:ws"},
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "OtherType", Path: "root:org:team:ws"},
						{Name: "SomeType", Path: "root:org:team:ws"},
					},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:   "Ready",
							Status: "True",
						},
						{
							Type:   "ExtensionsResolved",
							Status: "True",
						},
						{
							Type:   "RelationshipsValid",
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
			name: "parent type does not allow us as a child",
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "OtherType", Path: "root:org:team:ws"},
					},
				},
			},
			cwts: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "OtherType",
						ClusterName: "root:org:team:ws",
					},
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "OtherType", Path: "root:org:team:ws"},
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					TypeAliases:  []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "SomeType", Path: "root:org:team:ws"}},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:     "Ready",
							Status:   "False",
							Severity: "Error",
							Reason:   "ErrorValidatingRelationships",
							Message:  "parent root:org:team:ws:OtherType does not allow root:org:team:ws:SomeType as a child",
						},
						{
							Type:   "ExtensionsResolved",
							Status: "True",
						},
						{
							Type:     "RelationshipsValid",
							Status:   "False",
							Severity: "Error",
							Reason:   "ErrorValidatingRelationships",
							Message:  "parent root:org:team:ws:OtherType does not allow root:org:team:ws:SomeType as a child",
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
			name: "parent type allows anything as a child",
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "OtherType", Path: "root:org:team:ws"},
					},
				},
			},
			cwts: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "OtherType",
						ClusterName: "root:org:team:ws",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							tenancyv1alpha1.AnyWorkspaceTypeReference,
						},
					},
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "OtherType", Path: "root:org:team:ws"},
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					TypeAliases:  []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "SomeType", Path: "root:org:team:ws"}},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:   "Ready",
							Status: "True",
						},
						{
							Type:   "ExtensionsResolved",
							Status: "True",
						},
						{
							Type:   "RelationshipsValid",
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
			name: "parent type explicitly allows us as a child",
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "OtherType", Path: "root:org:team:ws"},
					},
				},
			},
			cwts: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "OtherType",
						ClusterName: "root:org:team:ws",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "SomeType", Path: "root:org:team:ws"},
						},
					},
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "OtherType", Path: "root:org:team:ws"},
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					TypeAliases:  []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "SomeType", Path: "root:org:team:ws"}},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:   "Ready",
							Status: "True",
						},
						{
							Type:   "ExtensionsResolved",
							Status: "True",
						},
						{
							Type:   "RelationshipsValid",
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
			name: "parent type implicitly allows us as a child through an alias",
			cwt: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "OtherType", Path: "root:org:team:ws"},
						},
					},
					AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "OtherType", Path: "root:org:team:ws"},
					},
				},
			},
			cwts: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "OtherType",
						ClusterName: "root:org:team:ws",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "OtherType", Path: "root:org:team:ws"},
						},
					},
				},
			},
			expected: &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "someType",
					ClusterName: "root:org:team:ws",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Name: "OtherType", Path: "root:org:team:ws"},
						},
					},
					AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "OtherType", Path: "root:org:team:ws"},
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "OtherType", Path: "root:org:team:ws"},
						{Name: "SomeType", Path: "root:org:team:ws"},
					},
					Conditions: conditionsv1alpha1.Conditions{
						{
							Type:   "Ready",
							Status: "True",
						},
						{
							Type:   "ExtensionsResolved",
							Status: "True",
						},
						{
							Type:   "RelationshipsValid",
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
				listClusterWorkspaceShards: func() ([]*tenancyv1alpha1.ClusterWorkspaceShard, error) {
					return testCase.shards, testCase.listErr
				},
				resolveClusterWorkspaceType: func(reference tenancyv1alpha1.ClusterWorkspaceTypeReference) (*tenancyv1alpha1.ClusterWorkspaceType, error) {
					if testCase.getErr != nil {
						return nil, testCase.getErr
					}
					for _, cwt := range testCase.cwts {
						if tenancyv1alpha1.ReferenceFor(cwt).Equal(reference) {
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
