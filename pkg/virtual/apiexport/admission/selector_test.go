/*
Copyright 2025 The KCP Authors.

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

package admission

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
)

const (
	apiDomainKey = "foo/bar"
)

var (
	scheme = runtime.NewScheme()
)

func apiBindingMatchAll(state apisv1alpha2.AcceptablePermissionClaimState) *apisv1alpha2.APIBinding {
	return &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cool-something",
		},
		Spec: apisv1alpha2.APIBindingSpec{
			PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
				{
					ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
						PermissionClaim: apisv1alpha2.PermissionClaim{
							GroupResource: apisv1alpha2.GroupResource{
								Group:    "",
								Resource: "configmaps",
							},
						},
						Selector: apisv1alpha2.PermissionClaimSelector{
							MatchAll: true,
						},
					},
					State: state,
				},
			},
		},
	}
}

func apiBindingMatchLabels(state apisv1alpha2.AcceptablePermissionClaimState) *apisv1alpha2.APIBinding {
	return &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cool-something",
		},
		Spec: apisv1alpha2.APIBindingSpec{
			PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
				{
					ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
						PermissionClaim: apisv1alpha2.PermissionClaim{
							GroupResource: apisv1alpha2.GroupResource{
								Group:    "",
								Resource: "configmaps",
							},
						},
						Selector: apisv1alpha2.PermissionClaimSelector{
							LabelSelector: metav1.LabelSelector{
								MatchLabels: map[string]string{
									"env": "test",
								},
							},
						},
					},
					State: state,
				},
			},
		},
	}
}

func apiBindingMatchExpressions(state apisv1alpha2.AcceptablePermissionClaimState) *apisv1alpha2.APIBinding {
	return &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cool-something",
		},
		Spec: apisv1alpha2.APIBindingSpec{
			PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
				{
					ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
						PermissionClaim: apisv1alpha2.PermissionClaim{
							GroupResource: apisv1alpha2.GroupResource{
								Group:    "",
								Resource: "configmaps",
							},
						},
						Selector: apisv1alpha2.PermissionClaimSelector{
							LabelSelector: metav1.LabelSelector{
								MatchExpressions: []metav1.LabelSelectorRequirement{
									{
										Key:      "env",
										Operator: metav1.LabelSelectorOpIn,
										Values:   []string{"test1", "test2"},
									},
								},
							},
						},
					},
					State: apisv1alpha2.ClaimAccepted,
				},
			},
		},
	}
}

func init() {
	scheme.AddKnownTypes(corev1.SchemeGroupVersion,
		&corev1.ConfigMap{},
	)
}

func toUnstructuredOrDie(obj runtime.Object) *unstructured.Unstructured {
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		panic(err)
	}
	u := &unstructured.Unstructured{Object: raw}

	kinds, _, err := scheme.ObjectKinds(obj)
	if err != nil {
		panic(err)
	}
	if len(kinds) == 0 {
		panic(fmt.Errorf("unable to determine kind for %T", obj))
	}
	u.SetKind(kinds[0].Kind)
	u.SetAPIVersion(kinds[0].GroupVersion().String())

	return u
}

func createAttr(obj runtime.Object, resource schema.GroupVersionResource) admission.Attributes {
	u := toUnstructuredOrDie(obj)

	return admission.NewAttributesRecord(
		u,
		nil,
		u.GroupVersionKind(),
		u.GetNamespace(),
		u.GetName(),
		resource,
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func updateAttr(obj runtime.Object, resource schema.GroupVersionResource) admission.Attributes {
	u := toUnstructuredOrDie(obj)

	return admission.NewAttributesRecord(
		u,
		u,
		u.GroupVersionKind(),
		u.GetNamespace(),
		u.GetName(),
		resource,
		"",
		admission.Update,
		&metav1.UpdateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func TestAdmit(t *testing.T) {
	cases := map[string]struct {
		apidomainKey          string
		resource              schema.GroupVersionResource
		obj                   runtime.Object
		update                bool
		wantLabels            map[string]string
		wantError             string
		getAPIBindingByExport func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error)
	}{
		"valid matchAll": {
			apidomainKey: apiDomainKey,
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cool-something",
					Namespace: metav1.NamespaceDefault,
				},
				Data: map[string]string{
					"test": "test",
				},
			},
			update:     false,
			wantLabels: nil,
			wantError:  "",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return apiBindingMatchAll(apisv1alpha2.ClaimAccepted), nil
			},
		},
		"valid matchLabels, object without labels": {
			apidomainKey: apiDomainKey,
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cool-something",
					Namespace: metav1.NamespaceDefault,
				},
				Data: map[string]string{
					"test": "test",
				},
			},
			update: false,
			wantLabels: map[string]string{
				"env": "test",
			},
			wantError: "",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return apiBindingMatchLabels(apisv1alpha2.ClaimAccepted), nil
			},
		},
		"valid matchLabels, object without labels, update op": {
			apidomainKey: apiDomainKey,
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cool-something",
					Namespace: metav1.NamespaceDefault,
				},
				Data: map[string]string{
					"test": "test",
				},
			},
			update: true,
			wantLabels: map[string]string{
				"env": "test",
			},
			wantError: "",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return apiBindingMatchLabels(apisv1alpha2.ClaimAccepted), nil
			},
		},
		"valid matchLabels, object with prior labels": {
			apidomainKey: apiDomainKey,
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cool-something",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						"test": "test",
					},
				},
				Data: map[string]string{
					"test": "test",
				},
			},
			update: false,
			wantLabels: map[string]string{
				"env":  "test",
				"test": "test",
			},
			wantError: "",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return apiBindingMatchLabels(apisv1alpha2.ClaimAccepted), nil
			},
		},
		"valid matchLabels, object with selector label": {
			apidomainKey: apiDomainKey,
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cool-something",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						"env":  "test",
						"test": "test",
					},
				},
				Data: map[string]string{
					"test": "test",
				},
			},
			update: false,
			wantLabels: map[string]string{
				"env":  "test",
				"test": "test",
			},
			wantError: "",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return apiBindingMatchLabels(apisv1alpha2.ClaimAccepted), nil
			},
		},
		"valid matchExpressions": {
			apidomainKey: apiDomainKey,
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cool-something",
					Namespace: metav1.NamespaceDefault,
				},
				Data: map[string]string{
					"test": "test",
				},
			},
			update:     false,
			wantLabels: nil,
			wantError:  "",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return apiBindingMatchExpressions(apisv1alpha2.ClaimAccepted), nil
			},
		},
		"matchAll with rejected permission claim": {
			apidomainKey: apiDomainKey,
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cool-something",
					Namespace: metav1.NamespaceDefault,
				},
				Data: map[string]string{
					"test": "test",
				},
			},
			update:     false,
			wantLabels: nil,
			wantError:  "",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return apiBindingMatchAll(apisv1alpha2.ClaimRejected), nil
			},
		},
		"matchLabels with rejected permission claim": {
			apidomainKey: apiDomainKey,
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cool-something",
					Namespace: metav1.NamespaceDefault,
				},
				Data: map[string]string{
					"test": "test",
				},
			},
			update:     false,
			wantLabels: nil,
			wantError:  "",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return apiBindingMatchLabels(apisv1alpha2.ClaimRejected), nil
			},
		},
		"missing apiDomainKey": {
			apidomainKey: "",
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj:          &corev1.ConfigMap{},
			update:       false,
			wantLabels:   nil,
			wantError:    "error getting valid cluster from context:",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return nil, nil
			},
		},
		"malformed apiDomainKey": {
			apidomainKey: "foo",
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj:          &corev1.ConfigMap{},
			update:       false,
			wantLabels:   nil,
			wantError:    "invalid API domain key",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return nil, nil
			},
		},
		"matchLabels with protected label change": {
			apidomainKey: apiDomainKey,
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cool-something",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						"env": "prod",
					},
				},
				Data: map[string]string{
					"test": "test",
				},
			},
			update:     false,
			wantLabels: nil,
			wantError:  "protected label env must have value test",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return apiBindingMatchLabels(apisv1alpha2.ClaimAccepted), nil
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			lc, _ := logicalcluster.NewPath(tc.apidomainKey).Name()
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: lc})
			ctx = dynamiccontext.WithAPIDomainKey(ctx, dynamiccontext.APIDomainKey(tc.apidomainKey))

			var attr admission.Attributes
			if tc.update {
				attr = updateAttr(tc.obj, tc.resource)
			} else {
				attr = createAttr(tc.obj, tc.resource)
			}

			sa := &selectorAdmission{
				getAPIBindingByExport: tc.getAPIBindingByExport,
			}
			if err := sa.Admit(ctx, attr, nil); err != nil {
				require.Contains(t, err.Error(), tc.wantError)
				return
			}
			if tc.wantError != "" {
				t.Errorf("no error returned but expected: %s", tc.wantError)
			}

			gotLabels := toUnstructuredOrDie(attr.GetObject()).GetLabels()
			if !equality.Semantic.DeepEqual(gotLabels, tc.wantLabels) {
				t.Errorf("unexpected labels:\n%s", cmp.Diff(gotLabels, tc.wantLabels))
			}
		})
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]struct {
		apidomainKey          string
		resource              schema.GroupVersionResource
		obj                   runtime.Object
		update                bool
		wantError             string
		getAPIBindingByExport func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error)
	}{
		"valid matchAll": {
			apidomainKey: apiDomainKey,
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cool-something",
					Namespace: metav1.NamespaceDefault,
				},
				Data: map[string]string{
					"test": "test",
				},
			},
			update:    false,
			wantError: "",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return apiBindingMatchAll(apisv1alpha2.ClaimAccepted), nil
			},
		},
		"valid matchLabels with label on the object": {
			apidomainKey: apiDomainKey,
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cool-something",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						"env": "test",
					},
				},
				Data: map[string]string{
					"test": "test",
				},
			},
			update:    false,
			wantError: "",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return apiBindingMatchLabels(apisv1alpha2.ClaimAccepted), nil
			},
		},
		"valid matchLabels with label on the object, update op": {
			apidomainKey: apiDomainKey,
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cool-something",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						"env": "test",
					},
				},
				Data: map[string]string{
					"test": "test",
				},
			},
			update:    true,
			wantError: "",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return apiBindingMatchLabels(apisv1alpha2.ClaimAccepted), nil
			},
		},
		"rejected permission claim": {
			apidomainKey: apiDomainKey,
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cool-something",
					Namespace: metav1.NamespaceDefault,
				},
				Data: map[string]string{
					"test": "test",
				},
			},
			update:    false,
			wantError: "",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return apiBindingMatchLabels(apisv1alpha2.ClaimRejected), nil
			},
		},
		"valid matchLabels with selector and additional labels on the object": {
			apidomainKey: apiDomainKey,
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cool-something",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						"env":  "test",
						"test": "test",
					},
				},
				Data: map[string]string{
					"test": "test",
				},
			},
			update:    false,
			wantError: "",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return apiBindingMatchLabels(apisv1alpha2.ClaimAccepted), nil
			},
		},
		"valid matchExpressions, first label in selector": {
			apidomainKey: apiDomainKey,
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cool-something",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						"env": "test1",
					},
				},
				Data: map[string]string{
					"test": "test",
				},
			},
			update:    false,
			wantError: "",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return apiBindingMatchExpressions(apisv1alpha2.ClaimAccepted), nil
			},
		},
		"valid matchExpressions, second label in selector": {
			apidomainKey: apiDomainKey,
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cool-something",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						"env": "test2",
					},
				},
				Data: map[string]string{
					"test": "test",
				},
			},
			update:    false,
			wantError: "",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return apiBindingMatchExpressions(apisv1alpha2.ClaimAccepted), nil
			},
		},
		"missing apiDomainKey": {
			apidomainKey: "",
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj:          &corev1.ConfigMap{},
			update:       false,
			wantError:    "error getting valid cluster from context:",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return nil, nil
			},
		},
		"malformed apiDomainKey": {
			apidomainKey: "foo",
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj:          &corev1.ConfigMap{},
			update:       false,
			wantError:    "invalid API domain key",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return nil, nil
			},
		},
		"matchLabels, object missing labels": {
			apidomainKey: apiDomainKey,
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cool-something",
					Namespace: metav1.NamespaceDefault,
				},
				Data: map[string]string{
					"test": "test",
				},
			},
			update:    false,
			wantError: "object does not match required labels",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return apiBindingMatchLabels(apisv1alpha2.ClaimAccepted), nil
			},
		},
		"matchLabels, object having different labels": {
			apidomainKey: apiDomainKey,
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cool-something",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						"test": "test",
					},
				},
				Data: map[string]string{
					"test": "test",
				},
			},
			update:    false,
			wantError: "object does not match required labels",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return apiBindingMatchLabels(apisv1alpha2.ClaimAccepted), nil
			},
		},
		"matchLabels, object having wrong selector label value": {
			apidomainKey: apiDomainKey,
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cool-something",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						"env": "prod",
					},
				},
				Data: map[string]string{
					"test": "test",
				},
			},
			update:    false,
			wantError: "object does not match required labels",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return apiBindingMatchLabels(apisv1alpha2.ClaimAccepted), nil
			},
		},
		"matchExpressions, object having wrong selector label value": {
			apidomainKey: apiDomainKey,
			resource:     corev1.SchemeGroupVersion.WithResource("configmaps"),
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cool-something",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						"env": "prod",
					},
				},
				Data: map[string]string{
					"test": "test",
				},
			},
			update:    false,
			wantError: "object does not match required labels",
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return apiBindingMatchExpressions(apisv1alpha2.ClaimAccepted), nil
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			lc, _ := logicalcluster.NewPath(tc.apidomainKey).Name()
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: lc})
			ctx = dynamiccontext.WithAPIDomainKey(ctx, dynamiccontext.APIDomainKey(tc.apidomainKey))

			var attr admission.Attributes
			if tc.update {
				attr = updateAttr(tc.obj, tc.resource)
			} else {
				attr = createAttr(tc.obj, tc.resource)
			}

			sa := &selectorAdmission{
				getAPIBindingByExport: tc.getAPIBindingByExport,
			}
			if err := sa.Validate(ctx, attr, nil); err != nil {
				require.Contains(t, err.Error(), tc.wantError)
				return
			}
			if tc.wantError != "" {
				t.Errorf("no error returned but expected: %s", tc.wantError)
			}
		})
	}
}
