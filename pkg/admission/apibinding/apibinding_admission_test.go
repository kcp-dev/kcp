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

package apibinding

import (
	"context"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	"strings"
	"testing"
)

func createAttr(s *apisv1alpha1.APIBinding) admission.Attributes {
	return admission.NewAttributesRecord(
		s,
		nil,
		apisv1alpha1.Kind("APIBinding").WithVersion("v1alpha1"),
		"",
		s.Name,
		apisv1alpha1.Resource("apibindings").WithVersion("v1alpha1"),
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func updateAttr(s, old *apisv1alpha1.APIBinding) admission.Attributes {
	return admission.NewAttributesRecord(
		s,
		old,
		tenancyv1alpha1.Kind("APIBinding").WithVersion("v1alpha1"),
		"",
		s.Name,
		tenancyv1alpha1.Resource("apibindings").WithVersion("v1alpha1"),
		"",
		admission.Update,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name           string
		attr           admission.Attributes
		expectedErrors []string
	}{
		{
			name: "Create passes with no reference",
			attr: createAttr(
				newAPIBinding().APIBinding,
			),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := &apiBindingAdmission{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}

			ctx := request.WithCluster(context.Background(), request.Cluster{Name: "root:org"})

			err := o.Validate(ctx, tc.attr, nil)

			wantErr := len(tc.expectedErrors) > 0
			require.Equal(t, wantErr, err != nil)

			if err != nil {
				t.Logf("Got admission errors: %v", err)
				for _, expected := range tc.expectedErrors {
					if !strings.Contains(err.Error(), expected) {
						t.Errorf("expected error %q", expected)
					}
				}
			}
		})
	}
}
