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

package apibinding

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

type recordingAuthorizer struct {
	allow func(attr authorizer.Attributes) bool
}

func (a recordingAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	if a.allow(attr) {
		return authorizer.DecisionAllow, "", nil
	}
	return authorizer.DecisionNoOpinion, "denied by test", nil
}

func logicalClusterFor(clusterName string) *corev1alpha1.LogicalCluster {
	return &corev1alpha1.LogicalCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: corev1alpha1.LogicalClusterName,
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: clusterName,
			},
		},
	}
}

func TestCheckDefaultAPIBindingsAccess(t *testing.T) {
	t.Parallel()

	const localCluster = logicalcluster.Name("local-cluster")

	allowAll := func(authorizer.Attributes) bool { return true }
	denyAll := func(authorizer.Attributes) bool { return false }

	tests := []struct {
		name     string
		bindings []tenancyv1alpha1.APIExportReference

		getLogicalCluster func(path logicalcluster.Path) (*corev1alpha1.LogicalCluster, error)
		newAuthorizerErr  error
		allow             func(authorizer.Attributes) bool

		wantErr          bool
		wantExportPath   string
		wantAuthorizedIn []logicalcluster.Name
		wantExports      []string
	}{
		{
			name:             "empty path resolves to the local cluster",
			bindings:         []tenancyv1alpha1.APIExportReference{{Export: "some-export"}},
			allow:            allowAll,
			wantAuthorizedIn: []logicalcluster.Name{localCluster},
			wantExports:      []string{"some-export"},
		},
		{
			name:             "root path resolves to the root cluster",
			bindings:         []tenancyv1alpha1.APIExportReference{{Path: core.RootCluster.String(), Export: "tenancy.kcp.io"}},
			allow:            allowAll,
			wantAuthorizedIn: []logicalcluster.Name{core.RootCluster},
			wantExports:      []string{"tenancy.kcp.io"},
		},
		{
			name:     "non-root path resolves via the LogicalCluster it points at",
			bindings: []tenancyv1alpha1.APIExportReference{{Path: "root:org:provider", Export: "today-cowboys"}},
			getLogicalCluster: func(path logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
				require.Equal(t, "root:org:provider", path.String())
				return logicalClusterFor("provider-cluster"), nil
			},
			allow:            allowAll,
			wantAuthorizedIn: []logicalcluster.Name{"provider-cluster"},
			wantExports:      []string{"today-cowboys"},
		},
		{
			name:     "every binding is checked",
			bindings: []tenancyv1alpha1.APIExportReference{{Export: "first"}, {Path: core.RootCluster.String(), Export: "second"}},
			allow:    allowAll,
			wantAuthorizedIn: []logicalcluster.Name{
				localCluster,
				core.RootCluster,
			},
			wantExports: []string{"first", "second"},
		},
		{
			name:     "denied bind is rejected and names the export path",
			bindings: []tenancyv1alpha1.APIExportReference{{Path: "root:org:provider", Export: "today-cowboys"}},
			getLogicalCluster: func(logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
				return logicalClusterFor("provider-cluster"), nil
			},
			allow:          denyAll,
			wantErr:        true,
			wantExportPath: "root:org:provider:today-cowboys",
		},
		{
			name:     "a later binding without bind is rejected",
			bindings: []tenancyv1alpha1.APIExportReference{{Export: "allowed"}, {Export: "denied"}},
			allow: func(attr authorizer.Attributes) bool {
				return attr.GetName() != "denied"
			},
			wantErr:        true,
			wantExportPath: "denied",
		},
		{
			name:     "unresolvable path is rejected",
			bindings: []tenancyv1alpha1.APIExportReference{{Path: "root:org:provider", Export: "today-cowboys"}},
			getLogicalCluster: func(logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
				return nil, errors.New("not found")
			},
			allow:          allowAll,
			wantErr:        true,
			wantExportPath: "root:org:provider:today-cowboys",
		},
		{
			name:             "authorizer construction failure is rejected",
			bindings:         []tenancyv1alpha1.APIExportReference{{Path: core.RootCluster.String(), Export: "tenancy.kcp.io"}},
			newAuthorizerErr: errors.New("boom"),
			allow:            allowAll,
			wantErr:          true,
			wantExportPath:   "root:tenancy.kcp.io",
		},
		{
			name:  "no bindings is always allowed",
			allow: denyAll,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var authorizedIn []logicalcluster.Name
			var exports []string

			getLogicalCluster := tt.getLogicalCluster
			if getLogicalCluster == nil {
				getLogicalCluster = func(path logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
					t.Fatalf("unexpected LogicalCluster lookup for path %q", path)
					return nil, nil
				}
			}

			newAuthorizer := func(clusterName logicalcluster.Name) (authorizer.Authorizer, error) {
				if tt.newAuthorizerErr != nil {
					return nil, tt.newAuthorizerErr
				}
				authorizedIn = append(authorizedIn, clusterName)
				return recordingAuthorizer{allow: func(attr authorizer.Attributes) bool {
					require.Equal(t, "bind", attr.GetVerb())
					require.Equal(t, "apiexports", attr.GetResource())
					exports = append(exports, attr.GetName())
					return tt.allow(attr)
				}}, nil
			}

			u := &user.DefaultInfo{Name: "user-1"}
			err := CheckDefaultAPIBindingsAccess(context.Background(), u, localCluster, tt.bindings, getLogicalCluster, newAuthorizer)

			if !tt.wantErr {
				require.NoError(t, err)
				require.Equal(t, tt.wantAuthorizedIn, authorizedIn)
				require.Equal(t, tt.wantExports, exports)
				return
			}

			require.Error(t, err)
			var accessErr *DefaultAPIBindingAccessError
			require.ErrorAs(t, err, &accessErr)
			require.Equal(t, tt.wantExportPath, accessErr.ExportPath)
		})
	}
}

func TestDefaultAPIBindingAccessErrorMessage(t *testing.T) {
	t.Parallel()

	withPath := &DefaultAPIBindingAccessError{ExportPath: "root:org:provider:today-cowboys"}
	require.Equal(t, "no permission to bind to export root:org:provider:today-cowboys", withPath.Error())

	withoutPath := &DefaultAPIBindingAccessError{}
	require.Equal(t, "no permission to bind one or more of the default API bindings", withoutPath.Error())
}
