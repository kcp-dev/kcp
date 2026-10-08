/*
Copyright 2022 The kcp Authors.

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

package reservedmetadata

import (
	"context"
	"testing"

	v1 "k8s.io/api/core/v1"
	apiextensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/handlers"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/clustercachedresources"
	cachereplication "github.com/kcp-dev/kcp/pkg/reconciler/cache/replication"
	"github.com/kcp-dev/kcp/pkg/reconciler/migration/logicalclustermigration"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/workspace"
)

func newAttr(obj, oldObject runtime.Object, op admission.Operation, user user.Info) admission.Attributes {
	return admission.NewAttributesRecord(
		obj,
		oldObject,
		schema.GroupVersionKind{},
		"",
		"test",
		schema.GroupVersionResource{},
		"",
		op,
		&metav1.CreateOptions{},
		false,
		user,
	)
}

func TestAnnotationReservation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		key         string
		reservation reservation
	}{
		// workspace admissions used by webhooks
		{key: tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey, reservation: unreserved},
		{key: authorization.RequiredGroupsAnnotationKey, reservation: unreserved},
		{key: core.LogicalClusterPathAnnotationKey, reservation: unreserved},

		// storage layer keys
		{key: logicalcluster.AnnotationKey, reservation: unreserved},
		{key: shard.AnnotationKey, reservation: unreserved},
		{key: handlers.KCPOriginalAPIVersionAnnotation, reservation: unreserved},

		// system-managed annotations: written only by kcp components running with privileged
		// credentials
		{key: "internal.tenancy.kcp.io/cluster", reservation: systemReserved},
		{key: workspace.WorkspaceShardHashAnnotationKey, reservation: systemReserved}, //nolint:staticcheck
		{key: tenancyv1alpha1.LogicalClusterTypeAnnotationKey, reservation: systemReserved},
		{key: corev1alpha1.LogicalClusterShardAnnotationKey, reservation: systemReserved},
		{key: corev1alpha1.LogicalClusterInactiveAnnotationKey, reservation: unreserved},
		{key: corev1alpha1.LogicalClusterInactiveAnnotationKeyLegacy, reservation: systemReserved}, //nolint:staticcheck
		{key: logicalclustermigration.MigratingAnnotationKey, reservation: systemReserved},
		{key: corev1alpha1.ShardRepresentationAnnotationKey, reservation: systemReserved},
		{key: core.ReplicateAnnotationKey, reservation: systemReserved},
		{key: apisv1alpha1.AnnotationBoundCRDKey, reservation: systemReserved},
		{key: apisv1alpha1.AnnotationSchemaClusterKey, reservation: systemReserved},
		{key: apisv1alpha1.AnnotationSchemaNameKey, reservation: systemReserved},
		{key: apisv1alpha1.AnnotationSchemaStorageKey, reservation: systemReserved},
		{key: apisv1alpha1.AnnotationAPIIdentityKey, reservation: systemReserved},
		{key: apibinding.ResourceBindingsAnnotationKey, reservation: systemReserved},
		{key: cachereplication.AnnotationKeyOriginalResourceVersion, reservation: systemReserved},
		{key: cachereplication.AnnotationKeyOriginalResourceUID, reservation: systemReserved},
		{key: clustercachedresources.AnnotationResourceKind, reservation: systemReserved},
		{key: clustercachedresources.AnnotationResourceScope, reservation: systemReserved},
		{key: "cache.kcp.io/referenced-by", reservation: systemReserved},
		{key: "crd.kcp.io/partial-metadata", reservation: systemReserved},
		{key: apiextensionsapiserver.KcpValidateNameAnnotationKey, reservation: systemReserved},
		{key: apisv1alpha1.VersionPreservationAnnotationKeyPrefix + "v1", reservation: systemReserved},

		// admin-managed annotations: set manually by kcp admins
		{key: corev1alpha1.LogicalClusterMaxTotalObjectsAnnotationKey, reservation: adminReserved},
		{key: corev1alpha1.ShardUnschedulableAnnotationKey, reservation: adminReserved},

		// Unknown kcp.io-domain keys default to system-reserved including top-level domain
		{key: "kcp.io/some-future-annotation", reservation: systemReserved},
		{key: "unknown.kcp.io/key", reservation: systemReserved},
		{key: "kcp.io", reservation: systemReserved},

		// User-facing feature keys, set with end-user credentials
		{key: "experimental.quota.kcp.io/cluster-scoped", reservation: unreserved},
		{key: tenancyv1alpha1.ExperimentalDefaultAPIBindingLifecycleAnnotationKey, reservation: unreserved},
		{key: tenancyv1alpha1.ExperimentalWorkspaceMountAnnotationKey, reservation: unreserved},
		{key: apisv1alpha1.AnnotationAPIExportExtraKeyPrefix + "my-key", reservation: unreserved},
		{key: apisv1alpha2.APIExportEndpointSliceSkipAnnotation, reservation: unreserved},

		// v1alpha1<->v1alpha2 conversion round-trip annotation examples
		{key: apisv1alpha2.ResourceSchemasAnnotation, reservation: unreserved},
		{key: apisv1alpha2.PermissionClaimsAnnotation, reservation: unreserved},
		{key: apisv1alpha2.PermissionClaimsV1Alpha1Annotation, reservation: unreserved},
		{key: apisv1alpha2.AcceptablePermissionClaimsAnnotation, reservation: unreserved},
		{key: apisv1alpha2.DeletionPolicyAnnotation, reservation: unreserved},
		{key: apisv1alpha2.StatusPermissionClaimsAnnotation, reservation: unreserved},
		{key: apisv1alpha2.StatusAppliedClaimsAnnotation, reservation: unreserved},
		{key: apisv1alpha2.StatusPermissionClaimsV1Alpha1Annotation, reservation: unreserved},
		{key: apisv1alpha2.StatusAppliedClaimsV1Alpha1Annotation, reservation: unreserved},

		// non kcp.io domains are always unreserved
		{key: v1.LastAppliedConfigAnnotation, reservation: unreserved},
		{key: "example.com/foo", reservation: unreserved},
		{key: "mykcp.io/foo", reservation: unreserved},
		{key: "description", reservation: unreserved},
	} {
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()
			if got := reservationOf(tc.key, annotationRules); got != tc.reservation {
				t.Errorf("annotation %q was %s, should be %s", tc.key, got, tc.reservation)
			}
		})
	}
}

func TestLabelReservation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		key         string
		reservation reservation
	}{
		// written by mutating admission inside the end user's own request
		{key: apisv1alpha1.APIExportPermissionClaimLabelPrefix + "abc123", reservation: unreserved},
		{key: apisv1alpha1.InternalAPIBindingExportLabelKey, reservation: unreserved},

		// System-managed labels, written only by privileged kcp components.
		{key: tenancyv1alpha1.WorkspacePhaseLabel, reservation: systemReserved},
		{key: tenancyv1alpha1.WorkspaceInitializerLabelPrefix + "2eadcbf778956517ec99fd1c1c32a9b", reservation: systemReserved},
		{key: tenancyv1alpha1.WorkspaceTerminatorLabelPrefix + "2eadcbf778956517ec99fd1c1c32a9b", reservation: systemReserved},
		{key: "internal.apis.kcp.io/not-export", reservation: systemReserved},
		{key: logicalcluster.AnnotationKey, reservation: systemReserved},

		// Unknown kcp.io-domain keys default to system-reserved including top-level domain
		{key: "foo.kcp.io/bar", reservation: systemReserved},
		{key: "kcp.io/some-future-label", reservation: systemReserved},
		{key: "kcp.io", reservation: systemReserved},

		// Labels we use in e2e tests
		{key: "internal.kcp.io/e2e-test", reservation: unreserved},
		{key: "internal.kcp.io/test-initializer", reservation: unreserved},
		{key: "internal.kcp.io/not-e2e-test", reservation: systemReserved},

		// Non kcp.io domains are always unreserved
		{key: "app.kubernetes.io/name", reservation: unreserved},
		{key: "example.com/foo", reservation: unreserved},
		{key: "mykcp.io/foo", reservation: unreserved},
	} {
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()
			if got := reservationOf(tc.key, labelRules); got != tc.reservation {
				t.Errorf("label %q was %s, should be %s", tc.key, got, tc.reservation)
			}
		})
	}
}

func TestUserClearance(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		groups    []string
		clearance reservation
	}{
		{name: "no groups", groups: nil, clearance: unreserved},
		{name: "regular user groups", groups: []string{"system:authenticated", "mygroup"}, clearance: unreserved},
		{name: "system:masters", groups: []string{user.SystemPrivilegedGroup}, clearance: systemReserved},
		{name: "logical-cluster-admin", groups: []string{bootstrap.SystemLogicalClusterAdmin}, clearance: systemReserved},
		{name: "external-logical-cluster-admin", groups: []string{bootstrap.SystemExternalLogicalClusterAdmin}, clearance: systemReserved},
		{name: "workspace-bootstrapper", groups: []string{bootstrap.SystemKcpWorkspaceBootstrapper}, clearance: systemReserved},
		{name: "kcp-admin", groups: []string{bootstrap.SystemKcpAdminGroup}, clearance: adminReserved},
		{name: "kcp-admin and control-plane group", groups: []string{bootstrap.SystemKcpAdminGroup, user.SystemPrivilegedGroup}, clearance: systemReserved},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := userClearance(tc.groups); got != tc.clearance {
				t.Errorf("groups %v got clearance %s, want %s", tc.groups, got, tc.clearance)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	pod := func(annotations, labels map[string]string) *v1.Pod {
		return &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "foo", Annotations: annotations, Labels: labels}}
	}
	systemOnly := map[string]string{"unit.kcp.io/reserved": "x"}
	adminOnly := map[string]string{corev1alpha1.LogicalClusterMaxTotalObjectsAnnotationKey: "100"}
	open := map[string]string{"unreserved": "x"}

	for _, tc := range []struct {
		name    string
		attr    admission.Attributes
		wantErr string
	}{
		{
			name: "object without metadata is allowed",
			attr: newAttr(nil, nil, admission.Create, &user.DefaultInfo{}),
		},
		{
			name:    "inaccessible old object is treated as empty",
			attr:    newAttr(pod(systemOnly, nil), nil, admission.Update, &user.DefaultInfo{}),
			wantErr: "forbidden: modification of system-reserved annotation: \"unit.kcp.io/reserved\"",
		},
		{
			name: "system:masters may modify system-reserved metadata",
			attr: newAttr(pod(systemOnly, systemOnly), nil, admission.Create,
				&user.DefaultInfo{Groups: []string{user.SystemPrivilegedGroup}}),
		},
		{
			name: "logical-cluster-admin may modify system-reserved metadata",
			attr: newAttr(pod(systemOnly, systemOnly), nil, admission.Create,
				&user.DefaultInfo{Groups: []string{bootstrap.SystemLogicalClusterAdmin}}),
		},
		{
			name: "external-logical-cluster-admin may modify system-reserved metadata",
			attr: newAttr(pod(systemOnly, systemOnly), nil, admission.Create,
				&user.DefaultInfo{Groups: []string{bootstrap.SystemExternalLogicalClusterAdmin}}),
		},
		{
			name: "workspace-bootstrapper may modify system-reserved metadata",
			attr: newAttr(pod(systemOnly, systemOnly), nil, admission.Create,
				&user.DefaultInfo{Groups: []string{bootstrap.SystemKcpWorkspaceBootstrapper}}),
		},
		{
			name: "control-plane group among other groups still clears everything",
			attr: newAttr(pod(systemOnly, systemOnly), nil, admission.Create,
				&user.DefaultInfo{Groups: []string{"system:authenticated", user.SystemPrivilegedGroup}}),
		},
		{
			name: "kcp-admin may modify admin-reserved annotations",
			attr: newAttr(pod(adminOnly, nil), pod(nil, nil), admission.Update,
				&user.DefaultInfo{Groups: []string{bootstrap.SystemKcpAdminGroup}}),
		},
		{
			name: "kcp-admin may not modify system-reserved annotations",
			attr: newAttr(pod(systemOnly, nil), pod(nil, nil), admission.Update,
				&user.DefaultInfo{Groups: []string{bootstrap.SystemKcpAdminGroup}}),
			wantErr: "forbidden: modification of system-reserved annotation: \"unit.kcp.io/reserved\"",
		},
		{
			name:    "regular users may not modify admin-reserved annotations",
			attr:    newAttr(pod(adminOnly, nil), pod(nil, nil), admission.Update, &user.DefaultInfo{}),
			wantErr: "forbidden: modification of admin-reserved annotation: \"core.kcp.io/max-total-objects\"",
		},
		{
			name:    "regular users may not modify system-reserved annotations",
			attr:    newAttr(pod(systemOnly, nil), pod(nil, nil), admission.Update, &user.DefaultInfo{}),
			wantErr: "forbidden: modification of system-reserved annotation: \"unit.kcp.io/reserved\"",
		},
		{
			name:    "regular users may not modify system-reserved labels",
			attr:    newAttr(pod(nil, systemOnly), pod(nil, nil), admission.Update, &user.DefaultInfo{}),
			wantErr: "forbidden: modification of system-reserved label: \"unit.kcp.io/reserved\"",
		},
		{
			name:    "annotation violations are reported before label violations",
			attr:    newAttr(pod(systemOnly, systemOnly), pod(nil, nil), admission.Update, &user.DefaultInfo{}),
			wantErr: "forbidden: modification of system-reserved annotation: \"unit.kcp.io/reserved\"",
		},
		{
			name: "unreserved changes are allowed for regular users",
			attr: newAttr(pod(open, open), pod(nil, nil), admission.Update, &user.DefaultInfo{}),
		},
		{
			name: "unchanged reserved metadata is allowed for regular users",
			attr: newAttr(pod(systemOnly, systemOnly), pod(systemOnly, systemOnly), admission.Update, &user.DefaultInfo{}),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plugin := &reservedMetadata{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}

			gotErr := ""
			if err := plugin.Validate(context.Background(), tc.attr, nil); err != nil {
				gotErr = err.Error()
			}
			if gotErr != tc.wantErr {
				t.Errorf("want error %q, got %q", tc.wantErr, gotErr)
			}
		})
	}
}

func TestReservedModification(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		new       map[string]string
		old       map[string]string
		rules     []rule
		clearance reservation
		wantKey   string
		wantRes   reservation
	}{
		{
			name: "nil maps are no modification",
		},
		{
			name: "unchanged reserved key is no modification",
			new:  map[string]string{"unit.kcp.io/reserved": "x"},
			old:  map[string]string{"unit.kcp.io/reserved": "x"},
		},
		{
			name: "unchanged empty-valued reserved key is no modification",
			new:  map[string]string{"unit.kcp.io/reserved": ""},
			old:  map[string]string{"unit.kcp.io/reserved": ""},
		},
		{
			name:    "added reserved key is a modification",
			new:     map[string]string{"unit.kcp.io/reserved": "x"},
			wantKey: "unit.kcp.io/reserved",
			wantRes: systemReserved,
		},
		{
			name:    "added empty-valued reserved key is a modification",
			new:     map[string]string{"unit.kcp.io/reserved": ""},
			wantKey: "unit.kcp.io/reserved",
			wantRes: systemReserved,
		},
		{
			name:    "deleted reserved key is a modification",
			old:     map[string]string{"unit.kcp.io/reserved": "x"},
			wantKey: "unit.kcp.io/reserved",
			wantRes: systemReserved,
		},
		{
			name:    "deleted empty-valued reserved key is a modification",
			old:     map[string]string{"unit.kcp.io/reserved": ""},
			wantKey: "unit.kcp.io/reserved",
			wantRes: systemReserved,
		},
		{
			name:    "changed reserved value is a modification",
			new:     map[string]string{"unit.kcp.io/reserved": "new"},
			old:     map[string]string{"unit.kcp.io/reserved": "old"},
			wantKey: "unit.kcp.io/reserved",
			wantRes: systemReserved,
		},
		{
			name: "changed unreserved key is no modification",
			new:  map[string]string{"unreserved": "new"},
			old:  map[string]string{"unreserved": "old"},
		},
		{
			name: "changed unreserved key next to unchanged reserved key is no modification",
			new:  map[string]string{"unreserved": "new", "unit.kcp.io/reserved": "x"},
			old:  map[string]string{"unreserved": "old", "unit.kcp.io/reserved": "x"},
		},
		{
			name:      "admin clearance covers an admin-reserved change",
			new:       map[string]string{"unit.kcp.io/admin": "x"},
			rules:     []rule{exact(adminReserved, "unit.kcp.io/admin")},
			clearance: adminReserved,
		},
		{
			name:      "admin clearance does not cover a system-reserved change",
			new:       map[string]string{"unit.kcp.io/reserved": "x"},
			clearance: adminReserved,
			wantKey:   "unit.kcp.io/reserved",
			wantRes:   systemReserved,
		},
		{
			name:    "unreserved clearance does not cover an admin-reserved change",
			new:     map[string]string{"unit.kcp.io/admin": "x"},
			rules:   []rule{exact(adminReserved, "unit.kcp.io/admin")},
			wantKey: "unit.kcp.io/admin",
			wantRes: adminReserved,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotKey, gotRes := reservedModification(tc.new, tc.old, tc.rules, tc.clearance)
			if gotKey != tc.wantKey || gotRes != tc.wantRes {
				t.Errorf("got (%q, %s), want (%q, %s)", gotKey, gotRes, tc.wantKey, tc.wantRes)
			}
		})
	}
}
