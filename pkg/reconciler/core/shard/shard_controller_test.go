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

package shard

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

func TestReconcileMarksCordoned(t *testing.T) {
	t.Parallel()
	c := &Controller{}
	shard := &corev1alpha1.Shard{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "root",
			Annotations: map[string]string{corev1alpha1.ShardUnschedulableAnnotationKey: "true"},
		},
	}
	if err := c.reconcile(context.Background(), shard); err != nil {
		t.Fatal(err)
	}
	cond := conditions.Get(shard, corev1alpha1.ShardSchedulable)
	if cond == nil {
		t.Fatal("expected the Schedulable condition to be set")
	}
	if cond.Status != corev1.ConditionFalse {
		t.Errorf("expected Schedulable=False, got %s", cond.Status)
	}
	if cond.Reason != corev1alpha1.ShardReasonCordoned {
		t.Errorf("expected reason %q, got %q", corev1alpha1.ShardReasonCordoned, cond.Reason)
	}
}

func TestReconcileMarksSchedulableAgain(t *testing.T) {
	t.Parallel()
	c := &Controller{}
	shard := &corev1alpha1.Shard{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "root",
			Annotations: map[string]string{corev1alpha1.ShardUnschedulableAnnotationKey: "true"},
		},
	}
	if err := c.reconcile(context.Background(), shard); err != nil {
		t.Fatal(err)
	}
	delete(shard.Annotations, corev1alpha1.ShardUnschedulableAnnotationKey)
	if err := c.reconcile(context.Background(), shard); err != nil {
		t.Fatal(err)
	}
	cond := conditions.Get(shard, corev1alpha1.ShardSchedulable)
	if cond == nil {
		t.Fatal("expected the Schedulable condition to be set")
	}
	if cond.Status != corev1.ConditionTrue {
		t.Errorf("expected Schedulable=True after uncordon, got %s (reason %s)", cond.Status, cond.Reason)
	}
}
