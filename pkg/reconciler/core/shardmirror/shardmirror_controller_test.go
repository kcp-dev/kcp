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

package shardmirror

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
)

func authoritativeShard(name string) *corev1alpha1.Shard {
	return &corev1alpha1.Shard{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				logicalcluster.AnnotationKey:             "system:shard",
				"kcp.io/shard":                           name,
				"cache.kcp.io/original-resource-version": "42",
				"cache.kcp.io/original-resource-UID":     "abc",
			},
			Labels: map[string]string{"name": name, "region": "us-east-1"},
		},
		Spec: corev1alpha1.ShardSpec{
			BaseURL:             "https://" + name,
			ExternalURL:         "https://" + name,
			VirtualWorkspaceURL: "https://" + name,
		},
	}
}

func representation(name string) *corev1alpha1.Shard {
	r := representationFor(authoritativeShard(name))
	return r
}

type fakeActions struct {
	created, updated, statusUpdated *corev1alpha1.Shard
	deleted                         string
}

func newController(source, local *corev1alpha1.Shard, actions *fakeActions) *Controller {
	return &Controller{
		getSourceShard: func(name string) (*corev1alpha1.Shard, error) {
			if source == nil || source.Name != name {
				return nil, apierrors.NewNotFound(corev1alpha1.Resource("shards"), name)
			}
			return source, nil
		},
		getLocalShard: func(name string) (*corev1alpha1.Shard, error) {
			if local == nil || local.Name != name {
				return nil, apierrors.NewNotFound(corev1alpha1.Resource("shards"), name)
			}
			return local, nil
		},
		createShard: func(_ context.Context, shard *corev1alpha1.Shard) error {
			actions.created = shard
			return nil
		},
		updateShard: func(_ context.Context, shard *corev1alpha1.Shard) error {
			actions.updated = shard
			return nil
		},
		updateShardStatus: func(_ context.Context, shard *corev1alpha1.Shard) error {
			actions.statusUpdated = shard
			return nil
		},
		deleteShard: func(_ context.Context, name string) error {
			actions.deleted = name
			return nil
		},
	}
}

func TestReconcileCreatesRepresentation(t *testing.T) {
	t.Parallel()
	actions := &fakeActions{}
	c := newController(authoritativeShard("alpha"), nil, actions)
	if err := c.reconcile(context.Background(), "alpha"); err != nil {
		t.Fatal(err)
	}
	if actions.created == nil {
		t.Fatal("expected a representation to be created")
	}
	if actions.created.Annotations[corev1alpha1.ShardRepresentationAnnotationKey] == "" {
		t.Error("representation must carry the representation annotation")
	}
	for _, key := range []string{
		logicalcluster.AnnotationKey,
		"kcp.io/shard",
		"cache.kcp.io/original-resource-version",
		"cache.kcp.io/original-resource-UID",
	} {
		if _, ok := actions.created.Annotations[key]; ok {
			t.Errorf("cache bookkeeping annotation %q must be stripped", key)
		}
	}
	if actions.created.Labels["region"] != "us-east-1" {
		t.Error("labels must be mirrored")
	}
	if actions.created.Spec.BaseURL != "https://alpha" {
		t.Error("spec must be mirrored")
	}
}

func TestReconcileStompsManualEdit(t *testing.T) {
	t.Parallel()
	edited := representation("alpha")
	edited.Spec.BaseURL = "https://tampered"
	actions := &fakeActions{}
	c := newController(authoritativeShard("alpha"), edited, actions)
	if err := c.reconcile(context.Background(), "alpha"); err != nil {
		t.Fatal(err)
	}
	if actions.updated == nil {
		t.Fatal("expected the manual edit to be overwritten")
	}
	if actions.updated.Spec.BaseURL != "https://alpha" {
		t.Errorf("expected spec to be restored, got %q", actions.updated.Spec.BaseURL)
	}
}

func TestReconcileAdoptsLegacyObject(t *testing.T) {
	t.Parallel()
	legacy := authoritativeShard("alpha").DeepCopy() // self-registered pre-upgrade, no representation annotation
	legacy.Annotations = map[string]string{logicalcluster.AnnotationKey: "root"}
	actions := &fakeActions{}
	c := newController(authoritativeShard("alpha"), legacy, actions)
	if err := c.reconcile(context.Background(), "alpha"); err != nil {
		t.Fatal(err)
	}
	if actions.updated == nil {
		t.Fatal("expected the legacy object to be adopted")
	}
	if actions.updated.Annotations[corev1alpha1.ShardRepresentationAnnotationKey] == "" {
		t.Error("adopted object must carry the representation annotation")
	}
}

func TestReconcileInSync(t *testing.T) {
	t.Parallel()
	actions := &fakeActions{}
	c := newController(authoritativeShard("alpha"), representation("alpha"), actions)
	if err := c.reconcile(context.Background(), "alpha"); err != nil {
		t.Fatal(err)
	}
	if actions.created != nil || actions.updated != nil || actions.statusUpdated != nil || actions.deleted != "" {
		t.Errorf("expected no action, got %+v", actions)
	}
}

func TestReconcileSyncsStatus(t *testing.T) {
	t.Parallel()
	source := authoritativeShard("alpha")
	source.Status.Capacity = corev1.ResourceList{
		"workspaces": resource.MustParse("5"),
	}
	local := representation("alpha")
	local.Status = corev1alpha1.ShardStatus{}
	actions := &fakeActions{}
	c := newController(source, local, actions)
	if err := c.reconcile(context.Background(), "alpha"); err != nil {
		t.Fatal(err)
	}
	if actions.updated != nil {
		t.Error("meta/spec are in sync, only status should be updated")
	}
	if actions.statusUpdated == nil {
		t.Fatal("expected a status update")
	}
	if actions.statusUpdated.Status.Capacity == nil {
		t.Error("expected status.capacity to be mirrored")
	}
}

func TestReconcilePrunesOrphanedRepresentation(t *testing.T) {
	t.Parallel()
	actions := &fakeActions{}
	c := newController(nil, representation("alpha"), actions)
	if err := c.reconcile(context.Background(), "alpha"); err != nil {
		t.Fatal(err)
	}
	if actions.deleted != "alpha" {
		t.Errorf("expected the orphaned representation to be deleted, got %q", actions.deleted)
	}
}

func TestReconcileLeavesUnmanagedObjectsAlone(t *testing.T) {
	t.Parallel()
	synthetic := authoritativeShard("fake") // e.g. created by an e2e test in the root workspace
	synthetic.Annotations = map[string]string{logicalcluster.AnnotationKey: "root"}
	actions := &fakeActions{}
	c := newController(nil, synthetic, actions)
	if err := c.reconcile(context.Background(), "fake"); err != nil {
		t.Fatal(err)
	}
	if actions.deleted != "" || actions.updated != nil || actions.created != nil {
		t.Errorf("expected no action on an unmanaged shard object, got %+v", actions)
	}
}
