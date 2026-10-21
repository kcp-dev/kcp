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

package apiexportstoragerekey

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

func rekey(name string, phase migrationv1alpha1.APIExportStorageRekeyPhase) *migrationv1alpha1.APIExportStorageRekey {
	return &migrationv1alpha1.APIExportStorageRekey{
		ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(time.Now().Add(-3 * time.Hour))},
		Spec: migrationv1alpha1.APIExportStorageRekeySpec{
			Export: migrationv1alpha1.ExportReference{Name: "cowboys"},
		},
		Status: migrationv1alpha1.APIExportStorageRekeyStatus{Phase: phase},
	}
}

func completedAt(r *migrationv1alpha1.APIExportStorageRekey, at time.Time) *migrationv1alpha1.APIExportStorageRekey {
	r.Status.Conditions = conditionsv1alpha1.Conditions{{
		Type:               migrationv1alpha1.StorageRekeyDrained,
		Status:             corev1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(at),
	}}
	return r
}

func attrs(op admission.Operation, obj, old runtime.Object) admission.Attributes {
	return admission.NewAttributesRecord(
		obj, old,
		migrationv1alpha1.SchemeGroupVersion.WithKind("APIExportStorageRekey"),
		"", "rekey", migrationv1alpha1.SchemeGroupVersion.WithResource("apiexportstoragerekeys"),
		"", op, nil, false, &user.DefaultInfo{Name: "provider"},
	)
}

// newRegisteredPlugin instantiates the plugin exactly as the server does, so
// the tests exercise the registered handler (and the operations it handles)
// rather than a hand-built one.
func newRegisteredPlugin(t *testing.T, listRekeys func(logicalcluster.Name) ([]*migrationv1alpha1.APIExportStorageRekey, error)) *storageRekeyAdmission {
	t.Helper()
	plugins := admission.NewPlugins()
	Register(plugins)
	plugin, err := plugins.InitPlugin(PluginName, nil, admission.PluginInitializers{})
	if err != nil {
		t.Fatalf("failed to init plugin: %v", err)
	}
	o, ok := plugin.(*storageRekeyAdmission)
	if !ok {
		t.Fatalf("unexpected plugin type %T", plugin)
	}
	o.listRekeys = listRekeys
	o.featureEnabled = func() bool { return true }
	return o
}

func TestValidateRejectsCreateWhenFeatureDisabled(t *testing.T) {
	t.Parallel()
	o := newRegisteredPlugin(t, func(logicalcluster.Name) ([]*migrationv1alpha1.APIExportStorageRekey, error) {
		return nil, nil
	})
	o.featureEnabled = func() bool { return false }
	ctx := genericapirequest.WithCluster(context.Background(), genericapirequest.Cluster{Name: "root:provider"})

	err := o.Validate(ctx, attrs(admission.Create, rekey("rekey", ""), nil), nil)
	if err == nil || !strings.Contains(err.Error(), "feature gate is disabled") {
		t.Fatalf("expected create to be rejected while the feature gate is disabled, got %v", err)
	}

	// updates and deletes stay possible so existing objects can be cleaned up.
	if err := o.Validate(ctx, attrs(admission.Delete, nil, rekey("rekey", migrationv1alpha1.APIExportStorageRekeyCompleted)), nil); err != nil {
		t.Fatalf("expected delete to be allowed while the feature gate is disabled, got %v", err)
	}
}

func TestRegisteredHandlerOperations(t *testing.T) {
	t.Parallel()
	o := newRegisteredPlugin(t, nil)
	for _, op := range []admission.Operation{admission.Create, admission.Update, admission.Delete} {
		if !o.Handles(op) {
			t.Errorf("registered plugin must handle %s", op)
		}
	}
	if o.Handles(admission.Connect) {
		t.Error("registered plugin must not handle CONNECT")
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	withTarget := func(r *migrationv1alpha1.APIExportStorageRekey, target string) *migrationv1alpha1.APIExportStorageRekey {
		r.Spec.TargetKey = target
		return r
	}
	withResources := func(r *migrationv1alpha1.APIExportStorageRekey, grs ...apisv1alpha2.GroupResource) *migrationv1alpha1.APIExportStorageRekey {
		r.Spec.Resources = grs
		return r
	}

	scenarios := []struct {
		name     string
		existing []*migrationv1alpha1.APIExportStorageRekey
		attrs    admission.Attributes
		wantErr  string
	}{
		{
			name:  "create with no history is allowed",
			attrs: attrs(admission.Create, rekey("rekey", ""), nil),
		},
		{
			name:     "create is rejected while another re-keying of the export is active",
			existing: []*migrationv1alpha1.APIExportStorageRekey{rekey("first", migrationv1alpha1.APIExportStorageRekeyDraining)},
			attrs:    attrs(admission.Create, rekey("rekey", ""), nil),
			wantErr:  "is still active",
		},
		{
			name: "cooldown counts from completion, not from creation",
			// created 3h ago (outside the cooldown), but completed 10 minutes ago.
			existing: []*migrationv1alpha1.APIExportStorageRekey{completedAt(rekey("first", migrationv1alpha1.APIExportStorageRekeyCompleted), time.Now().Add(-10*time.Minute))},
			attrs:    attrs(admission.Create, rekey("rekey", ""), nil),
			wantErr:  "cooldown",
		},
		{
			name:     "create is allowed once the cooldown since completion has passed",
			existing: []*migrationv1alpha1.APIExportStorageRekey{completedAt(rekey("first", migrationv1alpha1.APIExportStorageRekeyCompleted), time.Now().Add(-2*time.Hour))},
			attrs:    attrs(admission.Create, rekey("rekey", ""), nil),
		},
		{
			name:     "a failed re-keying does not block a new one",
			existing: []*migrationv1alpha1.APIExportStorageRekey{rekey("first", migrationv1alpha1.APIExportStorageRekeyFailed)},
			attrs:    attrs(admission.Create, rekey("rekey", ""), nil),
		},
		{
			name: "changing the target key is rejected",
			attrs: attrs(admission.Update,
				withTarget(rekey("rekey", migrationv1alpha1.APIExportStorageRekeyDraining), "other"),
				rekey("rekey", migrationv1alpha1.APIExportStorageRekeyDraining)),
			wantErr: "spec is immutable",
		},
		{
			name: "changing the resources is rejected",
			attrs: attrs(admission.Update,
				withResources(rekey("rekey", migrationv1alpha1.APIExportStorageRekeyDraining), apisv1alpha2.GroupResource{Group: "wildwest.dev", Resource: "cowboys"}),
				rekey("rekey", migrationv1alpha1.APIExportStorageRekeyDraining)),
			wantErr: "spec is immutable",
		},
		{
			name: "an unchanged spec is allowed on update",
			attrs: attrs(admission.Update,
				rekey("rekey", migrationv1alpha1.APIExportStorageRekeyDraining),
				rekey("rekey", migrationv1alpha1.APIExportStorageRekeyPending)),
		},
		{
			name:    "deleting a Draining re-keying is rejected",
			attrs:   attrs(admission.Delete, nil, rekey("rekey", migrationv1alpha1.APIExportStorageRekeyDraining)),
			wantErr: "is Draining",
		},
		{
			name:  "deleting a Completed re-keying is allowed",
			attrs: attrs(admission.Delete, nil, rekey("rekey", migrationv1alpha1.APIExportStorageRekeyCompleted)),
		},
		{
			name:  "deleting a Failed re-keying is allowed",
			attrs: attrs(admission.Delete, nil, rekey("rekey", migrationv1alpha1.APIExportStorageRekeyFailed)),
		},
		{
			name:  "deleting a Pending re-keying is allowed",
			attrs: attrs(admission.Delete, nil, rekey("rekey", migrationv1alpha1.APIExportStorageRekeyPending)),
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			o := newRegisteredPlugin(t, func(logicalcluster.Name) ([]*migrationv1alpha1.APIExportStorageRekey, error) {
				return scenario.existing, nil
			})
			ctx := genericapirequest.WithCluster(context.Background(), genericapirequest.Cluster{Name: "root:provider"})

			// the server only invokes plugins for operations they handle; a
			// scenario whose operation is not handled would silently pass.
			if !o.Handles(scenario.attrs.GetOperation()) {
				t.Fatalf("registered plugin does not handle %s", scenario.attrs.GetOperation())
			}
			err := o.Validate(ctx, scenario.attrs, nil)
			switch {
			case scenario.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case scenario.wantErr != "" && err == nil:
				t.Fatalf("expected error containing %q, got none", scenario.wantErr)
			case scenario.wantErr != "" && !strings.Contains(err.Error(), scenario.wantErr):
				t.Fatalf("expected error containing %q, got %q", scenario.wantErr, err.Error())
			}
		})
	}
}
