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

package permissionclaims

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
)

// TestToLabelKeyAndValue ensures that TestToLabelKeyAndValue stays stable.
func TestToLabelKeyAndValue(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		exportCluster logicalcluster.Name
		exportName    string
		claim         apisv1alpha1.PermissionClaim
		wantKey       string
		wantValue     string
	}{
		"simple group/resource": {
			exportCluster: "root",
			exportName:    "tenancy.kcp.io",
			claim: apisv1alpha1.PermissionClaim{
				GroupResource: apisv1alpha1.GroupResource{
					Group:    "",
					Resource: "configmaps",
				},
				All: true,
			},
			wantKey:   "claimed.internal.apis.kcp.io/bmCdly9xXiUpEHe3ypvDwvXMTfoVZUE92mqAQf",
			wantValue: "b4Kg2cKyX1dvWWHXUOcqu8F80KpNo21FI5jWLW",
		},
		"with identity hash": {
			exportCluster: "abcd1234",
			exportName:    "my-export",
			claim: apisv1alpha1.PermissionClaim{
				GroupResource: apisv1alpha1.GroupResource{
					Group:    "apps",
					Resource: "deployments",
				},
				All:          true,
				IdentityHash: "deadbeef",
			},
			wantKey:   "claimed.internal.apis.kcp.io/phGj1BkmdNY3LKTrtFc1KGLYTBs8Nt50p8PIU",
			wantValue: "7XggH3MXyijgN6j4CSVJwNyW1IXlInPf07rJTL",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			key, value, err := ToLabelKeyAndValue(tc.exportCluster, tc.exportName, tc.claim)
			require.NoError(t, err)
			assert.Equal(t, tc.wantKey, key)
			assert.Equal(t, tc.wantValue, value)
		})
	}
}

// TestToReflexiveAPIBindingLabelKeyAndValue ensures that ToReflexiveAPIBindingLabelKeyAndValue stays stable.
func TestToReflexiveAPIBindingLabelKeyAndValue(t *testing.T) {
	t.Parallel()
	key, value := ToReflexiveAPIBindingLabelKeyAndValue("root", "tenancy.kcp.io")
	assert.Equal(t, "claimed.internal.apis.kcp.io/bmCdly9xXiUpEHe3ypvDwvXMTfoVZUE92mqAQf", key)
	assert.Equal(t, "LstvmbbzVDDOn90ZbhzSQO5U3DMCf88h1pZ", value)
}

// TestToAPIBindingExportLabelValue ensures that ToAPIBindingExportLabelValue stays stable.
func TestToAPIBindingExportLabelValue(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		cluster    logicalcluster.Name
		exportName string
		want       string
	}{
		"root tenancy":   {"root", "tenancy.kcp.io", "bmCdly9xXiUpEHe3ypvDwvXMTfoVZUE92mqAQf"},
		"foreign export": {"abcd1234", "my-export", "phGj1BkmdNY3LKTrtFc1KGLYTBs8Nt50p8PIU"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, ToAPIBindingExportLabelValue(tc.cluster, tc.exportName))
		})
	}
}

// TestExportHashIsConsistent ensures that ToLabelKeyAndValue and ToReflexiveAPIBindingLabelKeyAndValue produce equivalent output.
func TestExportHashIsConsistent(t *testing.T) {
	t.Parallel()
	cluster := logicalcluster.Name("root")
	exportName := "tenancy.kcp.io"

	keyFromLabel, _, err := ToLabelKeyAndValue(cluster, exportName, apisv1alpha1.PermissionClaim{
		GroupResource: apisv1alpha1.GroupResource{Resource: "configmaps"},
		All:           true,
	})
	require.NoError(t, err)
	keyFromReflexive, _ := ToReflexiveAPIBindingLabelKeyAndValue(cluster, exportName)
	valueFromBinding := ToAPIBindingExportLabelValue(cluster, exportName)

	assert.Equal(t, keyFromLabel, keyFromReflexive)
	assert.Equal(t, apisv1alpha1.APIExportPermissionClaimLabelPrefix+valueFromBinding, keyFromLabel)
}
