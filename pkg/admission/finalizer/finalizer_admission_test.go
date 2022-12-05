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

package finalizer

import (
	"context"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibindingdeletion"
)

func createAttr(apiBinding *apisv1alpha1.APIBinding) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(apiBinding),
		nil,
		apisv1alpha1.Kind("APIBinding").WithVersion("v1alpha1"),
		"",
		apiBinding.Name,
		apisv1alpha1.Resource("apibindings").WithVersion("v1alpha1"),
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}
func updateAttr(newAPIBinding, oldAPIBinding *apisv1alpha1.APIBinding) admission.Attributes {
	return updateAttrWithUserInfo(newAPIBinding, oldAPIBinding, &user.DefaultInfo{})
}

func updateAttrWithUserInfo(newAPIBinding, oldAPIBinding *apisv1alpha1.APIBinding, u *user.DefaultInfo) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(newAPIBinding),
		helpers.ToUnstructuredOrDie(oldAPIBinding),
		apisv1alpha1.Kind("APIBinding").WithVersion("v1alpha1"),
		"",
		newAPIBinding.Name,
		apisv1alpha1.Resource("apibindings").WithVersion("v1alpha1"),
		"",
		admission.Update,
		&metav1.CreateOptions{},
		false,
		u,
	)
}

func TestAdmit(t *testing.T) {
	tests := []struct {
		name           string
		attr           admission.Attributes
		expectedErrors []string
		expectedObject runtime.Object
	}{
		{
			name: "Create: adds finalizer",
			attr: createAttr(
				newAPIBinding().APIBinding,
			),
			expectedObject: helpers.ToUnstructuredOrDie(newAPIBinding().withFinalizer(apibindingdeletion.APIBindingFinalizer).APIBinding),
		},
		{
			name: "Update: adds finalizer if missing",
			attr: updateAttr(
				newAPIBinding().APIBinding,
				newAPIBinding().APIBinding,
			),
			expectedObject: helpers.ToUnstructuredOrDie(newAPIBinding().withFinalizer(apibindingdeletion.APIBindingFinalizer).APIBinding),
		},
		{
			name: "Update: adds finalizer if missing when removed",
			attr: updateAttr(
				newAPIBinding().APIBinding,
				newAPIBinding().withFinalizer(apibindingdeletion.APIBindingFinalizer).APIBinding,
			),
			expectedObject: helpers.ToUnstructuredOrDie(newAPIBinding().withFinalizer(apibindingdeletion.APIBindingFinalizer).APIBinding),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := &FinalizerPlugin{
				Handler: admission.NewHandler(admission.Create, admission.Update),

				FinalizerName: apibindingdeletion.APIBindingFinalizer,
				Resource:      apisv1alpha1.Resource("apibindings"),
			}

			ctx := request.WithCluster(context.Background(), request.Cluster{Name: logicalcluster.From(tc.attr.GetObject().(metav1.Object))})

			err := o.Admit(ctx, tc.attr, nil)

			wantErr := len(tc.expectedErrors) > 0
			require.Equal(t, wantErr, err != nil)

			if err != nil {
				t.Logf("Got admission errors: %v", err)
				for _, expected := range tc.expectedErrors {
					require.Contains(t, err.Error(), expected)
				}
			} else {
				require.Equal(t, tc.expectedObject, tc.attr.GetObject())
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name           string
		attr           admission.Attributes
		expectedErrors []string
	}{
		{
			name: "Create: fails without finalizer",
			attr: createAttr(
				newAPIBinding().APIBinding,
			),
			expectedErrors: []string{"finalizer apis.kcp.dev/apibinding-finalizer is required"},
		},
		{
			name: "Create: passes with finalizer",
			attr: createAttr(
				newAPIBinding().withFinalizer(apibindingdeletion.APIBindingFinalizer).APIBinding,
			),
		},
		{
			name: "Update: passes without finalizer as system:masters on delete",
			attr: updateAttrWithUserInfo(
				newAPIBinding().withDeletionTimestamp(time.Now()).APIBinding,
				newAPIBinding().withFinalizer(apibindingdeletion.APIBindingFinalizer).APIBinding,
				&user.DefaultInfo{
					Groups: []string{user.SystemPrivilegedGroup},
				},
			),
		},
		{
			name: "Update: fails without finalizer as user on delete",
			attr: updateAttr(
				newAPIBinding().withDeletionTimestamp(time.Now()).APIBinding,
				newAPIBinding().withFinalizer(apibindingdeletion.APIBindingFinalizer).APIBinding,
			),
			expectedErrors: []string{"removing the finalizer apis.kcp.dev/apibinding-finalizer is forbidden"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := &FinalizerPlugin{
				Handler:       admission.NewHandler(admission.Create, admission.Update),
				FinalizerName: apibindingdeletion.APIBindingFinalizer,
				Resource:      apisv1alpha1.Resource("apibindings"),
			}

			ctx := request.WithCluster(context.Background(), request.Cluster{Name: logicalcluster.From(tc.attr.GetObject().(metav1.Object))})

			err := o.Validate(ctx, tc.attr, nil)

			wantErr := len(tc.expectedErrors) > 0
			require.Equal(t, wantErr, err != nil, "unexpected error, wantErr=%v, got: %v", wantErr, err)

			if err != nil {
				t.Logf("Got admission errors: %v", err)
				for _, expected := range tc.expectedErrors {
					require.Contains(t, err.Error(), expected)
				}
			}
		})
	}
}

type bindingBuilder struct {
	*apisv1alpha1.APIBinding
}

func newAPIBinding() *bindingBuilder {
	return &bindingBuilder{
		APIBinding: &apisv1alpha1.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					logicalcluster.AnnotationKey: "root:org:ws",
				},
			},
		},
	}
}

func (b *bindingBuilder) withFinalizer(name string) *bindingBuilder {
	b.APIBinding.Finalizers = append(b.APIBinding.Finalizers, name)
	return b
}

func (b *bindingBuilder) withDeletionTimestamp(t time.Time) *bindingBuilder {
	b.APIBinding.DeletionTimestamp = &metav1.Time{Time: t}
	return b
}
