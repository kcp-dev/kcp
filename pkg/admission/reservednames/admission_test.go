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

package reservednames

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func createAttr(name string, obj runtime.Object, kind, resource string) admission.Attributes {
	return admission.NewAttributesRecord(
		obj,
		obj,
		tenancyv1alpha1.Kind(kind).WithVersion("v1alpha1"),
		"",
		name,
		tenancyv1alpha1.Resource(resource).WithVersion("v1alpha1"),
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func TestAdmission(t *testing.T) {
	cases := map[string]struct {
		attr admission.Attributes
		want error
	}{
		"ForbiddenRootCW": {
			attr: createAttr("root", &tenancyv1alpha1.ClusterWorkspace{}, "ClusterWorkspace", "clusterworkspaces"),
			want: field.Invalid(field.NewPath("metadata").Child("name"), "root", "name is reserved"),
		},
		"ForbiddenSystemCW": {
			attr: createAttr("system", &tenancyv1alpha1.ClusterWorkspace{}, "ClusterWorkspace", "clusterworkspaces"),
			want: field.Invalid(field.NewPath("metadata").Child("name"), "system", "name is reserved"),
		},
		"ValidCW": {
			attr: createAttr("cool-cw", &tenancyv1alpha1.ClusterWorkspace{}, "ClusterWorkspace", "clusterworkspaces"),
		},
		"ForbiddenAnyCWT": {
			attr: createAttr("any", &tenancyv1alpha1.WorkspaceType{}, "WorkspaceType", "workspacetypes"),
			want: field.Invalid(field.NewPath("metadata").Child("name"), "any", "name is reserved"),
		},
		"ForbiddenSystemCWT": {
			attr: createAttr("system", &tenancyv1alpha1.WorkspaceType{}, "WorkspaceType", "workspacetypes"),
			want: field.Invalid(field.NewPath("metadata").Child("name"), "system", "name is reserved"),
		},
		"ValidCWT": {
			attr: createAttr("cool-cwt", &tenancyv1alpha1.WorkspaceType{}, "WorkspaceType", "workspacetypes"),
		},
		"NotApplicableResource": {
			attr: createAttr("root", &tenancyv1alpha1.ClusterWorkspace{}, "ClusterWorkspace", "notacworcwt"),
		},
		"NotApplicableKind": {
			attr: createAttr("root", &tenancyv1alpha1.ClusterWorkspace{}, "NotaCWorCWT", "clusterworkspaces"),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			plugin := NewReservedNames()
			if err := plugin.Admit(context.Background(), tc.attr, nil); err != nil {
				require.Contains(t, err.Error(), tc.want.Error())
				return
			}
			if tc.want != nil {
				t.Errorf("no error returned but expected: %s", tc.want.Error())
			}
		})
	}
}
