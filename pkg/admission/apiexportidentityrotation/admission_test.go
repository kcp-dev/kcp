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

package apiexportidentityrotation

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

func rotation(name string, phase migrationv1alpha1.APIExportIdentityRotationPhase) *migrationv1alpha1.APIExportIdentityRotation {
	return &migrationv1alpha1.APIExportIdentityRotation{
		ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(time.Now().Add(-3 * time.Hour))},
		Spec: migrationv1alpha1.APIExportIdentityRotationSpec{
			Export:      migrationv1alpha1.ExportReference{Name: "cowboys"},
			NewIdentity: apisv1alpha2.Identity{SecretRef: &corev1.SecretReference{Namespace: "default", Name: "new"}},
		},
		Status: migrationv1alpha1.APIExportIdentityRotationStatus{Phase: phase},
	}
}

func completedAt(r *migrationv1alpha1.APIExportIdentityRotation, at time.Time) *migrationv1alpha1.APIExportIdentityRotation {
	r.Status.Conditions = conditionsv1alpha1.Conditions{{
		Type:               migrationv1alpha1.IdentityRotationAliasRetired,
		Status:             corev1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(at),
	}}
	return r
}

func attrs(op admission.Operation, obj, old runtime.Object) admission.Attributes {
	return admission.NewAttributesRecord(
		obj, old,
		migrationv1alpha1.SchemeGroupVersion.WithKind("APIExportIdentityRotation"),
		"", "rotate", migrationv1alpha1.SchemeGroupVersion.WithResource("apiexportidentityrotations"),
		"", op, nil, false, &user.DefaultInfo{Name: "provider"},
	)
}

// newRegisteredPlugin instantiates the plugin exactly as the server does, so
// the tests exercise the registered handler (and the operations it handles)
// rather than a hand-built one.
func newRegisteredPlugin(t *testing.T, listRotations func(logicalcluster.Name) ([]*migrationv1alpha1.APIExportIdentityRotation, error)) *identityRotationAdmission {
	t.Helper()
	plugins := admission.NewPlugins()
	Register(plugins)
	plugin, err := plugins.InitPlugin(PluginName, nil, admission.PluginInitializers{})
	if err != nil {
		t.Fatalf("failed to init plugin: %v", err)
	}
	o, ok := plugin.(*identityRotationAdmission)
	if !ok {
		t.Fatalf("unexpected plugin type %T", plugin)
	}
	o.listRotations = listRotations
	o.featureEnabled = func() bool { return true }
	return o
}

func TestValidateRejectsCreateWhenFeatureDisabled(t *testing.T) {
	t.Parallel()
	o := newRegisteredPlugin(t, func(logicalcluster.Name) ([]*migrationv1alpha1.APIExportIdentityRotation, error) {
		return nil, nil
	})
	o.featureEnabled = func() bool { return false }
	ctx := genericapirequest.WithCluster(context.Background(), genericapirequest.Cluster{Name: "root:provider"})

	err := o.Validate(ctx, attrs(admission.Create, rotation("rotate", ""), nil), nil)
	if err == nil || !strings.Contains(err.Error(), "feature gate is disabled") {
		t.Fatalf("expected create to be rejected while the feature gate is disabled, got %v", err)
	}

	// updates and deletes stay possible so existing rotations can be cleaned up.
	if err := o.Validate(ctx, attrs(admission.Delete, nil, rotation("rotate", migrationv1alpha1.APIExportIdentityRotationCompleted)), nil); err != nil {
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

	withAfter := func(r *migrationv1alpha1.APIExportIdentityRotation, after *metav1.Duration) *migrationv1alpha1.APIExportIdentityRotation {
		r.Spec.AliasRetirement = migrationv1alpha1.AliasRetirement{Policy: migrationv1alpha1.AliasRetirementAfter, After: after}
		return r
	}

	scenarios := []struct {
		name     string
		existing []*migrationv1alpha1.APIExportIdentityRotation
		attrs    admission.Attributes
		wantErr  string
	}{
		{
			name:  "create with no history is allowed",
			attrs: attrs(admission.Create, rotation("rotate", ""), nil),
		},
		{
			name:     "create is rejected while another rotation of the export is active",
			existing: []*migrationv1alpha1.APIExportIdentityRotation{rotation("first", migrationv1alpha1.APIExportIdentityRotationMigrating)},
			attrs:    attrs(admission.Create, rotation("rotate", ""), nil),
			wantErr:  "is still active",
		},
		{
			name: "cooldown counts from completion, not from creation",
			// created 3h ago (outside the cooldown), but completed 10 minutes ago.
			existing: []*migrationv1alpha1.APIExportIdentityRotation{completedAt(rotation("first", migrationv1alpha1.APIExportIdentityRotationCompleted), time.Now().Add(-10*time.Minute))},
			attrs:    attrs(admission.Create, rotation("rotate", ""), nil),
			wantErr:  "cooldown",
		},
		{
			name:     "create is allowed once the cooldown since completion has passed",
			existing: []*migrationv1alpha1.APIExportIdentityRotation{completedAt(rotation("first", migrationv1alpha1.APIExportIdentityRotationCompleted), time.Now().Add(-2*time.Hour))},
			attrs:    attrs(admission.Create, rotation("rotate", ""), nil),
		},
		{
			name:    "After policy requires a duration",
			attrs:   attrs(admission.Create, withAfter(rotation("rotate", ""), nil), nil),
			wantErr: "spec.aliasRetirement.after must be a positive duration",
		},
		{
			name:    "After policy rejects a zero duration",
			attrs:   attrs(admission.Create, withAfter(rotation("rotate", ""), &metav1.Duration{}), nil),
			wantErr: "spec.aliasRetirement.after must be a positive duration",
		},
		{
			name:  "After policy with a duration is allowed",
			attrs: attrs(admission.Create, withAfter(rotation("rotate", ""), &metav1.Duration{Duration: time.Hour}), nil),
		},
		{
			name: "update switching to After without a duration is rejected",
			attrs: attrs(admission.Update,
				withAfter(rotation("rotate", migrationv1alpha1.APIExportIdentityRotationAliasActive), nil),
				rotation("rotate", migrationv1alpha1.APIExportIdentityRotationAliasActive)),
			wantErr: "spec.aliasRetirement.after must be a positive duration",
		},
		{
			name:    "deleting a Migrating rotation is rejected",
			attrs:   attrs(admission.Delete, nil, rotation("rotate", migrationv1alpha1.APIExportIdentityRotationMigrating)),
			wantErr: "is Migrating",
		},
		{
			name:    "deleting an AliasActive rotation is rejected",
			attrs:   attrs(admission.Delete, nil, rotation("rotate", migrationv1alpha1.APIExportIdentityRotationAliasActive)),
			wantErr: "is AliasActive",
		},
		{
			name:  "deleting a Completed rotation is allowed",
			attrs: attrs(admission.Delete, nil, rotation("rotate", migrationv1alpha1.APIExportIdentityRotationCompleted)),
		},
		{
			name:  "deleting a Failed rotation is allowed",
			attrs: attrs(admission.Delete, nil, rotation("rotate", migrationv1alpha1.APIExportIdentityRotationFailed)),
		},
		{
			name:  "deleting a Pending rotation is allowed",
			attrs: attrs(admission.Delete, nil, rotation("rotate", migrationv1alpha1.APIExportIdentityRotationPending)),
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			o := newRegisteredPlugin(t, func(logicalcluster.Name) ([]*migrationv1alpha1.APIExportIdentityRotation, error) {
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
