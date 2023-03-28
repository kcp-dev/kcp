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

package webhook

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/plugin/webhook"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
)

func attr(gvk schema.GroupVersionKind, name, resource string, op admission.Operation) admission.Attributes {
	obj := unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	obj.SetName(name)
	return admission.NewAttributesRecord(
		&obj,
		nil,
		obj.GroupVersionKind(),
		"",
		obj.GetName(),
		obj.GroupVersionKind().GroupVersion().WithResource(resource),
		"",
		op,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

type validatingDispatcher struct {
	hooks map[logicalcluster.Name][]webhook.WebhookAccessor
}

func (d *validatingDispatcher) Dispatch(ctx context.Context, a admission.Attributes, o admission.ObjectInterfaces, hooks []webhook.WebhookAccessor) error {
	if len(hooks) != len(d.hooks) {
		return fmt.Errorf("invalid number of hooks sent to dispatcher")
	}
	uidMatches := map[string]*struct{}{}
	for _, h := range hooks {
		for _, allHooks := range d.hooks {
			for _, expectedHook := range allHooks {
				if h.GetUID() == expectedHook.GetUID() {
					uidMatches[h.GetUID()] = &struct{}{}
				}
			}
		}
	}
	if len(uidMatches) != len(d.hooks) {
		return fmt.Errorf("hooks UID did not match expected")
	}
	return nil
}

type fakeHookSource struct {
	hooks     map[logicalcluster.Name][]webhook.WebhookAccessor
	hasSynced bool
}

func (f fakeHookSource) Webhooks(cluster logicalcluster.Name) []webhook.WebhookAccessor {
	return f.hooks[cluster]
}
func (f fakeHookSource) HasSynced() bool {
	return f.hasSynced
}

func TestDispatch(t *testing.T) {
	tests := []struct {
		name                string
		attr                admission.Attributes
		cluster             logicalcluster.Name
		expectedHooks       map[logicalcluster.Name][]webhook.WebhookAccessor
		hooksInSource       map[logicalcluster.Name][]webhook.WebhookAccessor
		hookSourceNotSynced bool
		apiBindings         []*apisv1alpha1.APIBinding
		apiExports          []*apisv1alpha1.APIExport
		informersHaveSynced func() bool
		wantErr             bool
	}{
		{
			name: "call for APIBinding only calls hooks in api registration logical cluster",
			attr: attr(
				schema.GroupVersionKind{Kind: "Cowboy", Group: "wildwest.dev", Version: "v1"},
				"bound-resource",
				"cowboys",
				admission.Create,
			),
			cluster: "root-org-dest",
			expectedHooks: map[logicalcluster.Name][]webhook.WebhookAccessor{
				logicalcluster.Name("root-org-source"): {webhook.NewValidatingWebhookAccessor("1", "api-registration-hook", nil)},
			},
			hooksInSource: map[logicalcluster.Name][]webhook.WebhookAccessor{
				logicalcluster.Name("root-org-source"): {webhook.NewValidatingWebhookAccessor("1", "api-registration-hook", nil)},
				logicalcluster.Name("root-org-dest"):   {webhook.NewValidatingWebhookAccessor("2", "secrets", nil)},
			},
			apiBindings: []*apisv1alpha1.APIBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "one",
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root-org-dest",
						},
					},
					Spec: apisv1alpha1.APIBindingSpec{
						Reference: apisv1alpha1.BindingReference{
							Export: &apisv1alpha1.ExportBindingReference{
								Path: "root:org:source",
								Name: "someExport",
							},
						},
					},
					Status: apisv1alpha1.APIBindingStatus{
						BoundResources: []apisv1alpha1.BoundAPIResource{
							{
								Group:    "wildwest.dev",
								Resource: "cowboys",
							},
						},
						APIExportClusterName: "root-org-source",
					},
				},
			},
			apiExports: []*apisv1alpha1.APIExport{
				newAPIExport(logicalcluster.NewPath("root:org:source"), "someExport").APIExport,
			},
		},
		{
			name: "call for resource only calls hooks in logical cluster",
			attr: attr(
				schema.GroupVersionKind{Kind: "Cowboy", Group: "wildwest.dev", Version: "v1"},
				"bound-resource",
				"cowboys",
				admission.Create,
			),
			cluster: "root-org-dest",
			expectedHooks: map[logicalcluster.Name][]webhook.WebhookAccessor{
				logicalcluster.Name("root-org-dest"): {webhook.NewValidatingWebhookAccessor("3", "secrets", nil)},
			},
			hooksInSource: map[logicalcluster.Name][]webhook.WebhookAccessor{
				logicalcluster.Name("root-org-source"): {
					webhook.NewValidatingWebhookAccessor("1", "cowboy-hook", nil),
					webhook.NewValidatingWebhookAccessor("2", "secrets", nil),
				},
				logicalcluster.Name("root-org-dest"): {webhook.NewValidatingWebhookAccessor("3", "secrets", nil)},
			},
		},
		{
			name: "API Bindings for other logical cluster call webhooks for dest cluster",
			attr: attr(
				schema.GroupVersionKind{Kind: "Cowboy", Group: "wildwest.dev", Version: "v1"},
				"bound-resource",
				"cowboys",
				admission.Create,
			),
			cluster: "root-org-dest",
			expectedHooks: map[logicalcluster.Name][]webhook.WebhookAccessor{
				logicalcluster.Name("root-org-dest"): {webhook.NewValidatingWebhookAccessor("3", "secrets", nil)},
			},
			hooksInSource: map[logicalcluster.Name][]webhook.WebhookAccessor{
				logicalcluster.Name("root-org-source"): {
					webhook.NewValidatingWebhookAccessor("1", "cowboy-hook", nil),
					webhook.NewValidatingWebhookAccessor("2", "secrets", nil),
				},
				logicalcluster.Name("root-org-dest"): {webhook.NewValidatingWebhookAccessor("3", "secrets", nil)},
			},
			apiBindings: []*apisv1alpha1.APIBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "two",
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root-org-dest",
						},
					},
					Status: apisv1alpha1.APIBindingStatus{
						BoundResources: []apisv1alpha1.BoundAPIResource{
							{
								Group:    "wildwest.dev",
								Resource: "Horses",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "one",
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root-org-dest-2",
						},
					},
					Status: apisv1alpha1.APIBindingStatus{
						BoundResources: []apisv1alpha1.BoundAPIResource{
							{
								Group:    "wildwest.dev",
								Resource: "Cowboys",
							},
						},
					},
				},
			},
		},
		{
			name: "API Bindings lister not synced",
			attr: attr(
				schema.GroupVersionKind{Kind: "Cowboy", Group: "wildwest.dev", Version: "v1"},
				"bound-resource",
				"cowboys",
				admission.Create,
			),
			cluster: "root-org-dest",
			informersHaveSynced: func() bool {
				return false
			},
			wantErr: true,
		},
		{
			name: "hook source not synced",
			attr: attr(
				schema.GroupVersionKind{Kind: "Cowboy", Group: "wildwest.dev", Version: "v1"},
				"bound-resource",
				"cowboys",
				admission.Create,
			),
			cluster:             "root-org-dest",
			hookSourceNotSynced: true,
			wantErr:             true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancelFn := context.WithCancel(context.Background())
			t.Cleanup(cancelFn)

			o := &WebhookDispatcher{
				Handler:             admission.NewHandler(admission.Connect, admission.Create, admission.Delete, admission.Update),
				dispatcher:          &validatingDispatcher{hooks: tc.expectedHooks},
				hookSource:          &fakeHookSource{hooks: tc.hooksInSource, hasSynced: !tc.hookSourceNotSynced},
				informersHaveSynced: tc.informersHaveSynced,
				getAPIBindings: func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error) {
					return tc.apiBindings, nil
				},
			}

			if tc.informersHaveSynced == nil {
				o.informersHaveSynced = func() bool { return true }
			}

			// Want to make sure that ready would fail based on these.
			o.SetReadyFunc(func() bool {
				return o.informersHaveSynced() && o.hookSource.HasSynced()
			})

			ctx = request.WithCluster(ctx, request.Cluster{Name: tc.cluster})
			if err := o.Dispatch(ctx, tc.attr, nil); (err != nil) != tc.wantErr {
				t.Fatalf("Dispatch() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

type apiExportBuilder struct {
	APIExport *apisv1alpha1.APIExport
}

func newAPIExport(path logicalcluster.Path, name string) apiExportBuilder {
	clusterName := strings.ReplaceAll(path.String(), ":", "-")
	return apiExportBuilder{
		APIExport: &apisv1alpha1.APIExport{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Annotations: map[string]string{
					logicalcluster.AnnotationKey:         clusterName,
					core.LogicalClusterPathAnnotationKey: path.String(),
				},
			},
		},
	}
}
