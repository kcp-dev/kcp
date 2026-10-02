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
	"regexp"
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

func TestIsPrivilegedAnnotation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		key        string
		privileged bool
	}{
		// workspace admissions used by webhooks
		{key: tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey, privileged: false},
		{key: authorization.RequiredGroupsAnnotationKey, privileged: false},
		{key: core.LogicalClusterPathAnnotationKey, privileged: false},

		// storage layer keys
		{key: logicalcluster.AnnotationKey, privileged: false},
		{key: shard.AnnotationKey, privileged: false},
		{key: handlers.KCPOriginalAPIVersionAnnotation, privileged: false},

		// system-managed annotations: written only by kcp components running with privileged
		// credentials
		{key: "internal.tenancy.kcp.io/cluster", privileged: true},
		{key: workspace.WorkspaceShardHashAnnotationKey, privileged: true}, //nolint:staticcheck
		{key: tenancyv1alpha1.LogicalClusterTypeAnnotationKey, privileged: true},
		{key: corev1alpha1.LogicalClusterShardAnnotationKey, privileged: true},
		{key: corev1alpha1.LogicalClusterMaxTotalObjectsAnnotationKey, privileged: false},
		{key: corev1alpha1.LogicalClusterInactiveAnnotationKey, privileged: false},
		{key: corev1alpha1.LogicalClusterInactiveAnnotationKeyLegacy, privileged: true}, //nolint:staticcheck
		{key: logicalclustermigration.MigratingAnnotationKey, privileged: true},
		{key: corev1alpha1.ShardRepresentationAnnotationKey, privileged: true},
		{key: core.ReplicateAnnotationKey, privileged: true},
		{key: corev1alpha1.ShardUnschedulableAnnotationKey, privileged: true},
		{key: apisv1alpha1.AnnotationBoundCRDKey, privileged: true},
		{key: apisv1alpha1.AnnotationSchemaClusterKey, privileged: true},
		{key: apisv1alpha1.AnnotationSchemaNameKey, privileged: true},
		{key: apisv1alpha1.AnnotationSchemaStorageKey, privileged: true},
		{key: apisv1alpha1.AnnotationAPIIdentityKey, privileged: true},
		{key: apibinding.ResourceBindingsAnnotationKey, privileged: true},
		{key: cachereplication.AnnotationKeyOriginalResourceVersion, privileged: true},
		{key: cachereplication.AnnotationKeyOriginalResourceUID, privileged: true},
		{key: clustercachedresources.AnnotationResourceKind, privileged: true},
		{key: clustercachedresources.AnnotationResourceScope, privileged: true},
		{key: "cache.kcp.io/referenced-by", privileged: true},
		{key: "crd.kcp.io/partial-metadata", privileged: true},
		{key: apiextensionsapiserver.KcpValidateNameAnnotationKey, privileged: true},
		{key: apisv1alpha1.VersionPreservationAnnotationKeyPrefix + "v1", privileged: true},

		// Unknown kcp.io-domain keys default to reserved including top-level domain
		{key: "kcp.io/some-future-annotation", privileged: true},
		{key: "unknown.kcp.io/key", privileged: true},
		{key: "kcp.io", privileged: true},

		// User-facing feature keys, set with end-user credentials
		{key: "experimental.quota.kcp.io/cluster-scoped", privileged: false},
		{key: tenancyv1alpha1.ExperimentalDefaultAPIBindingLifecycleAnnotationKey, privileged: false},
		{key: tenancyv1alpha1.ExperimentalWorkspaceMountAnnotationKey, privileged: false},
		{key: apisv1alpha1.AnnotationAPIExportExtraKeyPrefix + "my-key", privileged: false},
		{key: apisv1alpha2.APIExportEndpointSliceSkipAnnotation, privileged: false},

		// v1alpha1<->v1alpha2 conversion round-trip annotation examples
		{key: apisv1alpha2.ResourceSchemasAnnotation, privileged: false},
		{key: apisv1alpha2.PermissionClaimsAnnotation, privileged: false},
		{key: apisv1alpha2.PermissionClaimsV1Alpha1Annotation, privileged: false},
		{key: apisv1alpha2.AcceptablePermissionClaimsAnnotation, privileged: false},
		{key: apisv1alpha2.DeletionPolicyAnnotation, privileged: false},
		{key: apisv1alpha2.StatusPermissionClaimsAnnotation, privileged: false},
		{key: apisv1alpha2.StatusAppliedClaimsAnnotation, privileged: false},
		{key: apisv1alpha2.StatusPermissionClaimsV1Alpha1Annotation, privileged: false},
		{key: apisv1alpha2.StatusAppliedClaimsV1Alpha1Annotation, privileged: false},

		// non kcp.io domains are allowed
		{key: v1.LastAppliedConfigAnnotation, privileged: false},
		{key: "example.com/foo", privileged: false},
		{key: "mykcp.io/foo", privileged: false},
		{key: "description", privileged: false},
	} {
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()
			if got := isPrivileged(tc.key, annotationAllowList); got != tc.privileged {
				t.Errorf("annotation %q was %t, should be %t", tc.key, got, tc.privileged)
			}
		})
	}
}

func TestIsPrivilegedLabel(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		key        string
		privileged bool
	}{
		// Allowlisted: written by mutating admission inside the end user's own request.
		{key: apisv1alpha1.APIExportPermissionClaimLabelPrefix + "abc123", privileged: false},
		{key: apisv1alpha1.InternalAPIBindingExportLabelKey, privileged: false},

		// System-managed labels, written only by privileged kcp components.
		{key: tenancyv1alpha1.WorkspacePhaseLabel, privileged: true},
		{key: tenancyv1alpha1.WorkspaceInitializerLabelPrefix + "2eadcbf778956517ec99fd1c1c32a9b", privileged: true},
		{key: tenancyv1alpha1.WorkspaceTerminatorLabelPrefix + "2eadcbf778956517ec99fd1c1c32a9b", privileged: true},
		{key: "internal.apis.kcp.io/not-export", privileged: true},
		{key: logicalcluster.AnnotationKey, privileged: true},

		// Unknown kcp.io-domain keys default to reserved including top-level domain
		{key: "foo.kcp.io/bar", privileged: true},
		{key: "kcp.io/some-future-label", privileged: true},
		{key: "kcp.io", privileged: true},

		// Labels we use in e2e tests
		{key: "internal.kcp.io/e2e-test", privileged: false},
		{key: "internal.kcp.io/test-initializer", privileged: false},
		{key: "internal.kcp.io/not-e2e-test", privileged: true},

		// Non kcp.io domains are always allowed
		{key: "app.kubernetes.io/name", privileged: false},
		{key: "example.com/foo", privileged: false},
		{key: "mykcp.io/foo", privileged: false},
	} {
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()
			if got := isPrivileged(tc.key, labelAllowList); got != tc.privileged {
				t.Errorf("label %q was %t, should be %t", tc.key, got, tc.privileged)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	pod := func(annotations, labels map[string]string) *v1.Pod {
		return &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "foo", Annotations: annotations, Labels: labels}}
	}
	reserved := map[string]string{"unit.kcp.io/reserved": "x"}
	unreserved := map[string]string{"unreserved": "x"}

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
			attr:    newAttr(pod(reserved, nil), nil, admission.Update, &user.DefaultInfo{}),
			wantErr: "forbidden: modification of reserved annotation: \"unit.kcp.io/reserved\"",
		},
		{
			name: "system:masters bypasses all checks",
			attr: newAttr(pod(reserved, reserved), nil, admission.Create,
				&user.DefaultInfo{Groups: []string{user.SystemPrivilegedGroup}}),
		},
		{
			name: "logical-cluster-admin bypasses all checks",
			attr: newAttr(pod(reserved, reserved), nil, admission.Create,
				&user.DefaultInfo{Groups: []string{bootstrap.SystemLogicalClusterAdmin}}),
		},
		{
			name: "external-logical-cluster-admin bypasses all checks",
			attr: newAttr(pod(reserved, reserved), nil, admission.Create,
				&user.DefaultInfo{Groups: []string{bootstrap.SystemExternalLogicalClusterAdmin}}),
		},
		{
			name: "workspace-bootstrapper bypasses all checks",
			attr: newAttr(pod(reserved, reserved), nil, admission.Create,
				&user.DefaultInfo{Groups: []string{bootstrap.SystemKcpWorkspaceBootstrapper}}),
		},
		{
			name: "privileged group among other groups still bypasses",
			attr: newAttr(pod(reserved, reserved), nil, admission.Create,
				&user.DefaultInfo{Groups: []string{"system:authenticated", user.SystemPrivilegedGroup}}),
		},
		{
			name:    "reserved annotation change is forbidden for regular users",
			attr:    newAttr(pod(reserved, nil), pod(nil, nil), admission.Update, &user.DefaultInfo{}),
			wantErr: "forbidden: modification of reserved annotation: \"unit.kcp.io/reserved\"",
		},
		{
			name:    "reserved label change is forbidden for regular users",
			attr:    newAttr(pod(nil, reserved), pod(nil, nil), admission.Update, &user.DefaultInfo{}),
			wantErr: "forbidden: modification of reserved label: \"unit.kcp.io/reserved\"",
		},
		{
			name:    "annotation violations are reported before label violations",
			attr:    newAttr(pod(reserved, reserved), pod(nil, nil), admission.Update, &user.DefaultInfo{}),
			wantErr: "forbidden: modification of reserved annotation: \"unit.kcp.io/reserved\"",
		},
		{
			name: "unreserved changes are allowed for regular users",
			attr: newAttr(pod(unreserved, unreserved), pod(nil, nil), admission.Update, &user.DefaultInfo{}),
		},
		{
			name: "unchanged reserved metadata is allowed for regular users",
			attr: newAttr(pod(reserved, reserved), pod(reserved, reserved), admission.Update, &user.DefaultInfo{}),
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

func TestHasPrivilegedModification(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		new       map[string]string
		old       map[string]string
		allowList []*regexp.Regexp
		wantKey   string
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
		},
		{
			name:    "added empty-valued reserved key is a modification",
			new:     map[string]string{"unit.kcp.io/reserved": ""},
			wantKey: "unit.kcp.io/reserved",
		},
		{
			name:    "deleted reserved key is a modification",
			old:     map[string]string{"unit.kcp.io/reserved": "x"},
			wantKey: "unit.kcp.io/reserved",
		},
		{
			name:    "deleted empty-valued reserved key is a modification",
			old:     map[string]string{"unit.kcp.io/reserved": ""},
			wantKey: "unit.kcp.io/reserved",
		},
		{
			name:    "changed reserved value is a modification",
			new:     map[string]string{"unit.kcp.io/reserved": "new"},
			old:     map[string]string{"unit.kcp.io/reserved": "old"},
			wantKey: "unit.kcp.io/reserved",
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotKey, gotModified := hasPrivilegedModification(tc.new, tc.old, tc.allowList)
			if wantModified := tc.wantKey != ""; gotModified != wantModified || gotKey != tc.wantKey {
				t.Errorf("got (%q, %t), want (%q, %t)", gotKey, gotModified, tc.wantKey, tc.wantKey != "")
			}
		})
	}
}
