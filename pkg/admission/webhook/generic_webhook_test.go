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
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	webhookconfiguration "k8s.io/apiserver/pkg/admission/configuration"
	"k8s.io/apiserver/pkg/admission/plugin/webhook"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/client/clientset/versioned/fake"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
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
	hooks []webhook.WebhookAccessor
}

func (d *validatingDispatcher) Dispatch(ctx context.Context, a admission.Attributes, o admission.ObjectInterfaces, hooks []webhook.WebhookAccessor) error {
	if len(hooks) != len(d.hooks) {
		return fmt.Errorf("invalid number of hooks sent to dispatcher")
	}
	uidMatches := map[string]*struct{}{}
	for _, h := range hooks {
		for _, expectedHook := range d.hooks {
			if h.GetUID() == expectedHook.GetUID() {
				uidMatches[h.GetUID()] = &struct{}{}
			}
		}
	}
	if len(uidMatches) != len(d.hooks) {
		return fmt.Errorf("hooks UID did not match expected")
	}
	return nil
}

type fakeHookSource struct {
	hooks     []webhook.WebhookAccessor
	hasSynced bool
}

func (f fakeHookSource) Webhooks() []webhook.WebhookAccessor {
	return f.hooks

}
func (f fakeHookSource) HasSynced() bool {
	return f.hasSynced
}

func TestDispatch(t *testing.T) {
	tests := []struct {
		name                string
		attr                admission.Attributes
		cluster             string
		expectedHooks       []webhook.WebhookAccessor
		hooksInSource       []webhook.WebhookAccessor
		hookSourceNotSynced bool
		apiBindings         []*v1alpha1.APIBinding
		apiBindingsSynced   func() bool
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
			cluster: "root:org:dest-cluster",
			expectedHooks: []webhook.WebhookAccessor{
				webhookconfiguration.WithCluster(logicalcluster.New("root:org:source-cluster"), webhook.NewValidatingWebhookAccessor("1", "api-registration-hook", nil)),
			},
			hooksInSource: []webhook.WebhookAccessor{
				webhookconfiguration.WithCluster(logicalcluster.New("root:org:source-cluster"), webhook.NewValidatingWebhookAccessor("1", "api-registration-hook", nil)),
				webhookconfiguration.WithCluster(logicalcluster.New("root:org:dest-cluster"), webhook.NewValidatingWebhookAccessor("2", "secrets", nil)),
			},
			apiBindings: []*v1alpha1.APIBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "one",
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root:org:dest-cluster",
						},
					},
					Status: v1alpha1.APIBindingStatus{
						BoundResources: []v1alpha1.BoundAPIResource{
							{
								Group:    "wildwest.dev",
								Resource: "cowboys",
							},
						},
						BoundAPIExport: &v1alpha1.ExportReference{
							Workspace: &v1alpha1.WorkspaceExportReference{
								Path: "root:org:source-cluster",
							},
						},
					},
				},
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
			cluster: "root:org:dest-cluster",
			expectedHooks: []webhook.WebhookAccessor{
				webhookconfiguration.WithCluster(logicalcluster.New("root:org:dest-cluster"), webhook.NewValidatingWebhookAccessor("3", "secrets", nil)),
			},
			hooksInSource: []webhook.WebhookAccessor{
				webhookconfiguration.WithCluster(logicalcluster.New("root:org:source-cluster"), webhook.NewValidatingWebhookAccessor("1", "cowboy-hook", nil)),
				webhookconfiguration.WithCluster(logicalcluster.New("root:org:source-cluster"), webhook.NewValidatingWebhookAccessor("2", "secrets", nil)),
				webhookconfiguration.WithCluster(logicalcluster.New("root:org:dest-cluster"), webhook.NewValidatingWebhookAccessor("3", "secrets", nil)),
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
			cluster: "root:org:dest-cluster",
			expectedHooks: []webhook.WebhookAccessor{
				webhookconfiguration.WithCluster(logicalcluster.New("root:org:dest-cluster"), webhook.NewValidatingWebhookAccessor("3", "secrets", nil)),
			},
			hooksInSource: []webhook.WebhookAccessor{
				webhookconfiguration.WithCluster(logicalcluster.New("root:org:source-cluster"), webhook.NewValidatingWebhookAccessor("1", "cowboy-hook", nil)),
				webhookconfiguration.WithCluster(logicalcluster.New("root:org:source-cluster"), webhook.NewValidatingWebhookAccessor("2", "secrets", nil)),
				webhookconfiguration.WithCluster(logicalcluster.New("root:org:dest-cluster"), webhook.NewValidatingWebhookAccessor("3", "secrets", nil)),
			},
			apiBindings: []*v1alpha1.APIBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "two",
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root:org:dest-cluster",
						},
					},
					Status: v1alpha1.APIBindingStatus{
						BoundResources: []v1alpha1.BoundAPIResource{
							{
								Group:    "wildwest.dev",
								Resource: "Horses",
							},
						},
						BoundAPIExport: &v1alpha1.ExportReference{
							Workspace: &v1alpha1.WorkspaceExportReference{
								Path: "root:org:source-cluster",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "one",
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root:org:dest-cluster-2",
						},
					},
					Status: v1alpha1.APIBindingStatus{
						BoundResources: []v1alpha1.BoundAPIResource{
							{
								Group:    "wildwest.dev",
								Resource: "Cowboys",
							},
						},
						BoundAPIExport: &v1alpha1.ExportReference{
							Workspace: &v1alpha1.WorkspaceExportReference{
								Path: "root:org:source-cluster",
							},
						},
					},
				},
			},
		},
		{
			name: "API Bindings Lister not synced",
			attr: attr(
				schema.GroupVersionKind{Kind: "Cowboy", Group: "wildwest.dev", Version: "v1"},
				"bound-resource",
				"cowboys",
				admission.Create,
			),
			cluster: "root:org:dest-cluster",
			apiBindingsSynced: func() bool {
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
			cluster:             "root:org:dest-cluster",
			hookSourceNotSynced: true,
			wantErr:             true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancelFn := context.WithCancel(context.Background())
			t.Cleanup(cancelFn)

			fakeClient := fake.NewSimpleClientset(toObjects(tc.apiBindings)...)
			fakeInformerFactory := kcpinformers.NewSharedInformerFactory(fakeClient, time.Hour)
			err := fakeInformerFactory.Apis().V1alpha1().APIBindings().Informer().AddIndexers(cache.Indexers{
				byWorkspaceIndex: func(obj interface{}) ([]string, error) {
					return []string{logicalcluster.From(obj.(metav1.Object)).String()}, nil
				},
			})
			if err != nil {
				t.Errorf("unable to add indexer to fake informer-%v", err)
			}

			o := &WebhookDispatcher{
				Handler:              admission.NewHandler(admission.Connect, admission.Create, admission.Delete, admission.Update),
				dispatcher:           &validatingDispatcher{hooks: tc.expectedHooks},
				hookSource:           &fakeHookSource{hooks: tc.hooksInSource, hasSynced: !tc.hookSourceNotSynced},
				apiBindingsIndexer:   fakeInformerFactory.Apis().V1alpha1().APIBindings().Informer().GetIndexer(),
				apiBindingsHasSynced: tc.apiBindingsSynced,
			}

			fakeInformerFactory.Start(ctx.Done())
			fakeInformerFactory.WaitForCacheSync(ctx.Done())

			if tc.apiBindingsSynced == nil {
				o.apiBindingsHasSynced = func() bool { return true }
			}

			// Want to make sure that ready would fail based on these.
			o.SetReadyFunc(func() bool {
				return o.apiBindingsHasSynced() && o.hookSource.HasSynced()
			})

			ctx = request.WithCluster(ctx, request.Cluster{Name: logicalcluster.New(tc.cluster)})
			if err := o.Dispatch(ctx, tc.attr, nil); (err != nil) != tc.wantErr {
				t.Fatalf("Dispatch() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func toObjects(bindings []*v1alpha1.APIBinding) []runtime.Object {
	objs := make([]runtime.Object, 0, len(bindings))
	for _, binding := range bindings {
		objs = append(objs, binding)
	}
	return objs
}
