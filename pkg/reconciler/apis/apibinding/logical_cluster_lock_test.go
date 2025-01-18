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

package apibinding

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
)

func TestWithLockedResources(t *testing.T) {
	now := time.Now().UTC()
	expired := time.Now().Add(-1).UTC().Format(time.RFC3339)
	notExpired := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)

	tests := map[string]struct {
		lc      *corev1alpha1.LogicalCluster
		crds    []*apiextensionsv1.CustomResourceDefinition
		grs     []schema.GroupResource
		binding ExpirableLock

		want        *corev1alpha1.LogicalCluster
		wantLocked  []schema.GroupResource
		wantSkipped map[schema.GroupResource]Lock
		wantErr     bool
	}{
		"no annotation errors": {
			lc:      &corev1alpha1.LogicalCluster{},
			grs:     []schema.GroupResource{{Group: "group", Resource: "foos"}},
			binding: ExpirableLock{Lock: Lock{Name: "binding"}},
			wantErr: true,
		},
		"empty annotation errors": {
			lc:      newLogicalClusterWithAnnotation(""),
			grs:     []schema.GroupResource{{Group: "group", Resource: "foos"}},
			binding: ExpirableLock{Lock: Lock{Name: "binding"}},
			wantErr: true,
		},
		"invalid annotation errors": {
			lc:      newLogicalClusterWithAnnotation("invalid"),
			grs:     []schema.GroupResource{{Group: "group", Resource: "foos"}},
			binding: ExpirableLock{Lock: Lock{Name: "binding"}},
			wantErr: true,
		},
		"empty annotation succeeds": {
			lc:          newLogicalClusterWithAnnotation("{}"),
			grs:         []schema.GroupResource{{Group: "group", Resource: "foos"}},
			binding:     ExpirableLock{Lock: Lock{Name: "binding"}},
			want:        newLogicalClusterWithAnnotation(`{"foos.group":{"n":"binding"}}`),
			wantLocked:  []schema.GroupResource{{Group: "group", Resource: "foos"}},
			wantSkipped: map[schema.GroupResource]Lock{},
		},
		"existing annotation without existing bindings succeeds": {
			lc:          newLogicalClusterWithAnnotation(`{"bars.group":{"n":"another"},"crds.group":{"c":true},"foos.group":{"n":"binding"}}`),
			grs:         []schema.GroupResource{{Group: "group", Resource: "foos"}},
			binding:     ExpirableLock{Lock: Lock{Name: "binding"}},
			want:        newLogicalClusterWithAnnotation(`{"bars.group":{"n":"another"},"crds.group":{"c":true},"foos.group":{"n":"binding"}}`),
			wantLocked:  []schema.GroupResource{{Group: "group", Resource: "foos"}},
			wantSkipped: map[schema.GroupResource]Lock{},
		},
		"existing annotation with conflicting binding fails": {
			lc:          newLogicalClusterWithAnnotation(`{"bars.group":{"n":"another"},"crds.group":{"c":true},"foos.group":{"n":"another"}}`),
			grs:         []schema.GroupResource{{Group: "group", Resource: "foos"}},
			binding:     ExpirableLock{Lock: Lock{Name: "binding"}},
			want:        newLogicalClusterWithAnnotation(`{"bars.group":{"n":"another"},"crds.group":{"c":true},"foos.group":{"n":"another"}}`),
			wantLocked:  []schema.GroupResource{},
			wantSkipped: map[schema.GroupResource]Lock{{Group: "group", Resource: "foos"}: {Name: "another"}},
		},
		"existing annotation with conflicting CRD binding fails": {
			lc:          newLogicalClusterWithAnnotation(`{"bars.group":{"n":"another"},"crds.group":{"c":true},"foos.group":{"c":true}}`),
			crds:        []*apiextensionsv1.CustomResourceDefinition{newCRD("group", "foos")},
			grs:         []schema.GroupResource{{Group: "group", Resource: "foos"}},
			binding:     ExpirableLock{Lock: Lock{Name: "binding"}},
			want:        newLogicalClusterWithAnnotation(`{"bars.group":{"n":"another"},"crds.group":{"c":true},"foos.group":{"c":true}}`),
			wantLocked:  []schema.GroupResource{},
			wantSkipped: map[schema.GroupResource]Lock{{Group: "group", Resource: "foos"}: {CRD: true}},
		},
		"existing annotation with expired conflicting CRD binding and CRD exists": {
			lc:          newLogicalClusterWithAnnotation(fmt.Sprintf(`{"bars.group":{"n":"another"},"crds.group":{"c":true},"foos.group":{"c":true,"e":%q}}`, expired)),
			crds:        []*apiextensionsv1.CustomResourceDefinition{newCRD("group", "foos")},
			grs:         []schema.GroupResource{{Group: "group", Resource: "foos"}},
			binding:     ExpirableLock{Lock: Lock{Name: "binding"}},
			want:        newLogicalClusterWithAnnotation(fmt.Sprintf(`{"bars.group":{"n":"another"},"crds.group":{"c":true},"foos.group":{"c":true,"e":%q}}`, expired)),
			wantLocked:  []schema.GroupResource{},
			wantSkipped: map[schema.GroupResource]Lock{{Group: "group", Resource: "foos"}: {CRD: true}},
		},
		"existing annotation with expired conflicting CRD binding and CRD does not exist": {
			lc:          newLogicalClusterWithAnnotation(fmt.Sprintf(`{"bars.group":{"n":"another"},"crds.group":{"c":true},"foos.group":{"c":true,"e":%q}}`, expired)),
			grs:         []schema.GroupResource{{Group: "group", Resource: "foos"}},
			binding:     ExpirableLock{Lock: Lock{Name: "binding"}},
			want:        newLogicalClusterWithAnnotation(`{"bars.group":{"n":"another"},"crds.group":{"c":true},"foos.group":{"n":"binding"}}`),
			wantLocked:  []schema.GroupResource{{Group: "group", Resource: "foos"}},
			wantSkipped: map[schema.GroupResource]Lock{},
		},
		"existing annotation with not expired, but conflicting CRD binding and CRD does not exist": {
			lc:          newLogicalClusterWithAnnotation(fmt.Sprintf(`{"bars.group":{"n":"another"},"crds.group":{"c":true},"foos.group":{"c":true,"e":%q}}`, notExpired)),
			crds:        []*apiextensionsv1.CustomResourceDefinition{newCRD("group", "bars")},
			grs:         []schema.GroupResource{{Group: "group", Resource: "foos"}},
			binding:     ExpirableLock{Lock: Lock{Name: "binding"}},
			want:        newLogicalClusterWithAnnotation(fmt.Sprintf(`{"bars.group":{"n":"another"},"crds.group":{"c":true},"foos.group":{"c":true,"e":%q}}`, notExpired)),
			wantLocked:  []schema.GroupResource{},
			wantSkipped: map[schema.GroupResource]Lock{{Group: "group", Resource: "foos"}: {CRD: true}},
		},
		"multiple resources, all without conflict": {
			lc:          newLogicalClusterWithAnnotation(`{"crds.group":{"c":true}}`),
			grs:         []schema.GroupResource{{Group: "group", Resource: "foos"}, {Group: "group", Resource: "bars"}},
			binding:     ExpirableLock{Lock: Lock{Name: "binding"}},
			want:        newLogicalClusterWithAnnotation(`{"bars.group":{"n":"binding"},"crds.group":{"c":true},"foos.group":{"n":"binding"}}`),
			wantLocked:  []schema.GroupResource{{Group: "group", Resource: "foos"}, {Group: "group", Resource: "bars"}},
			wantSkipped: map[schema.GroupResource]Lock{},
		},
		"multiple resources, one with conflict": {
			lc:          newLogicalClusterWithAnnotation(`{"crds.group":{"c":true},"foos.group":{"n":"another"}}`),
			grs:         []schema.GroupResource{{Group: "group", Resource: "foos"}, {Group: "group", Resource: "bars"}},
			binding:     ExpirableLock{Lock: Lock{Name: "binding"}},
			want:        newLogicalClusterWithAnnotation(`{"bars.group":{"n":"binding"},"crds.group":{"c":true},"foos.group":{"n":"another"}}`),
			wantLocked:  []schema.GroupResource{{Group: "group", Resource: "bars"}},
			wantSkipped: map[schema.GroupResource]Lock{{Group: "group", Resource: "foos"}: {Name: "another"}},
		},
		"binding a crd": {
			lc:          newLogicalClusterWithAnnotation(`{"crds.group":{"c":true}}`),
			grs:         []schema.GroupResource{{Group: "group", Resource: "foos"}},
			binding:     ExpirableLock{Lock: Lock{CRD: true}},
			want:        newLogicalClusterWithAnnotation(`{"crds.group":{"c":true},"foos.group":{"c":true}}`),
			wantLocked:  []schema.GroupResource{{Group: "group", Resource: "foos"}},
			wantSkipped: map[schema.GroupResource]Lock{},
		},
		"binding a crd again": {
			lc:          newLogicalClusterWithAnnotation(`{"crds.group":{"c":true},"foos.group":{"c":true}}`),
			grs:         []schema.GroupResource{{Group: "group", Resource: "foos"}},
			binding:     ExpirableLock{Lock: Lock{CRD: true}},
			want:        newLogicalClusterWithAnnotation(`{"crds.group":{"c":true},"foos.group":{"c":true}}`),
			wantLocked:  []schema.GroupResource{{Group: "group", Resource: "foos"}},
			wantSkipped: map[schema.GroupResource]Lock{},
		},
		"binding a crd with expiry again": {
			lc:          newLogicalClusterWithAnnotation(fmt.Sprintf(`{"crds.group":{"c":true},"foos.group":{"c":true,"e":%q}}`, expired)),
			grs:         []schema.GroupResource{{Group: "group", Resource: "foos"}},
			binding:     ExpirableLock{Lock: Lock{CRD: true}, CRDExpiry: &metav1.Time{Time: now}},
			want:        newLogicalClusterWithAnnotation(fmt.Sprintf(`{"crds.group":{"c":true},"foos.group":{"c":true,"e":%q}}`, now.Format(time.RFC3339))),
			wantLocked:  []schema.GroupResource{{Group: "group", Resource: "foos"}},
			wantSkipped: map[schema.GroupResource]Lock{},
		},
		"binding a crd with conflicting binding": {
			lc:          newLogicalClusterWithAnnotation(`{"crds.group":{"c":true},"foos.group":{"n":"another"}}`),
			grs:         []schema.GroupResource{{Group: "group", Resource: "foos"}},
			binding:     ExpirableLock{Lock: Lock{CRD: true}},
			want:        newLogicalClusterWithAnnotation(`{"crds.group":{"c":true},"foos.group":{"n":"another"}}`),
			wantLocked:  []schema.GroupResource{},
			wantSkipped: map[schema.GroupResource]Lock{{Group: "group", Resource: "foos"}: {Name: "another"}},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, locked, skipped, err := WithLockedResources(tt.crds, now, tt.lc, tt.grs, tt.binding)
			if (err != nil) != tt.wantErr {
				t.Errorf("WithLockedResources() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("WithLockedResources() +got -want\n%s", diff)
			}
			if diff := cmp.Diff(locked, tt.wantLocked); diff != "" {
				t.Errorf("WithLockedResources() +got -want:\n%s", diff)
			}
			if diff := cmp.Diff(skipped, tt.wantSkipped); diff != "" {
				t.Errorf("WithLockedResources() +got -want:\n%s", diff)
			}
		})
	}
}

func newLogicalClusterWithAnnotation(ann string) *corev1alpha1.LogicalCluster {
	return &corev1alpha1.LogicalCluster{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				ResourceBindingsAnnotationKey: ann,
			},
		},
	}
}
