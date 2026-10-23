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

package apiexport

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"

	"github.com/kcp-dev/sdk/apis/apis"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
)

func statusAttr(oldHash, newHash string, groups ...string) admission.Attributes {
	export := func(hash string) *apisv1alpha2.APIExport {
		return &apisv1alpha2.APIExport{
			ObjectMeta: metav1.ObjectMeta{Name: "cool-something"},
			Status:     apisv1alpha2.APIExportStatus{IdentityHash: hash},
		}
	}
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(export(newHash)),
		helpers.ToUnstructuredOrDie(export(oldHash)),
		apisv1alpha2.Kind("APIExport").WithVersion("v1alpha2"),
		"",
		"cool-something",
		apisv1alpha2.Resource("apiexports").WithVersion("v1alpha2"),
		"status",
		admission.Update,
		&metav1.UpdateOptions{},
		false,
		&user.DefaultInfo{Name: "someone", Groups: groups},
	)
}

func TestStatusIdentityHashImmutable(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		oldHash, newHash string
		groups           []string
		wantErr          string
	}{
		"first assignment is allowed":           {oldHash: "", newHash: "abc"},
		"unchanged is allowed":                  {oldHash: "abc", newHash: "abc"},
		"other status fields may change":        {oldHash: "", newHash: ""},
		"changing it is forbidden":              {oldHash: "abc", newHash: "def", wantErr: "identityHash is immutable once set"},
		"clearing it is forbidden":              {oldHash: "abc", newHash: "", wantErr: "identityHash is immutable once set"},
		"kcp itself may withdraw it":            {oldHash: "abc", newHash: "", groups: []string{user.SystemPrivilegedGroup}},
		"kcp itself may change it":              {oldHash: "abc", newHash: "def", groups: []string{user.SystemPrivilegedGroup}},
		"a workspace admin group does not help": {oldHash: "abc", newHash: "def", groups: []string{"system:kcp:workspace:access", "system:authenticated"}, wantErr: "identityHash is immutable once set"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			plugin := NewAPIExportAdmission(func(apis.GroupResource) bool { return false })
			err := plugin.Validate(context.Background(), statusAttr(tc.oldHash, tc.newHash, tc.groups...), nil)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
