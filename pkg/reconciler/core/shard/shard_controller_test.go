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
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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

func TestReconcileReportsResourceLimits(t *testing.T) {
	t.Parallel()

	list := func(pairs map[string]string) corev1.ResourceList {
		if len(pairs) == 0 {
			return nil
		}
		l := corev1.ResourceList{}
		for name, quantity := range pairs {
			l[corev1.ResourceName(name)] = resource.MustParse(quantity)
		}
		return l
	}

	scenarios := []struct {
		name        string
		limits      *corev1alpha1.ShardResourceLimits
		wantStatus  corev1.ConditionStatus
		wantReason  string
		wantMessage string
	}{
		{
			name:        "no limits configured",
			limits:      nil,
			wantStatus:  corev1.ConditionTrue,
			wantMessage: "no limits configured",
		},
		{
			name:        "empty limits read as none configured",
			limits:      &corev1alpha1.ShardResourceLimits{},
			wantStatus:  corev1.ConditionTrue,
			wantMessage: "no limits configured",
		},
		{
			name: "soft and hard are both reported",
			limits: &corev1alpha1.ShardResourceLimits{
				Soft: list(map[string]string{"workspaces": "10"}),
				Hard: list(map[string]string{"workspaces": "20"}),
			},
			wantStatus:  corev1.ConditionTrue,
			wantMessage: "soft/hard: workspaces=10/20",
		},
		{
			name: "an unconfigured tier renders as a dash",
			limits: &corev1alpha1.ShardResourceLimits{
				Hard: list(map[string]string{"workspaces": "20"}),
			},
			wantStatus:  corev1.ConditionTrue,
			wantMessage: "soft/hard: workspaces=-/20",
		},
		{
			name: "zero is a configured but disabled limit",
			limits: &corev1alpha1.ShardResourceLimits{
				Soft: list(map[string]string{"workspaces": "0"}),
				Hard: list(map[string]string{"workspaces": "0"}),
			},
			wantStatus:  corev1.ConditionTrue,
			wantMessage: "soft/hard: workspaces=0/0",
		},
		{
			name: "several resources are sorted by name",
			limits: &corev1alpha1.ShardResourceLimits{
				Soft: list(map[string]string{"workspaces": "10", "apibindings": "5"}),
				Hard: list(map[string]string{"workspaces": "20", "apibindings": "9"}),
			},
			wantStatus:  corev1.ConditionTrue,
			wantMessage: "soft/hard: apibindings=5/9, workspaces=10/20",
		},
		{
			name: "a negative limit keeps the values in the message",
			limits: &corev1alpha1.ShardResourceLimits{
				Soft: list(map[string]string{"workspaces": "-1"}),
				Hard: list(map[string]string{"workspaces": "20"}),
			},
			wantStatus:  corev1.ConditionFalse,
			wantReason:  corev1alpha1.ShardReasonInvalidResourceLimits,
			wantMessage: "soft/hard: workspaces=-1/20; soft workspaces=-1 is negative and is treated as disabled",
		},
		{
			name: "a soft limit at the hard limit keeps the values in the message",
			limits: &corev1alpha1.ShardResourceLimits{
				Soft: list(map[string]string{"workspaces": "20"}),
				Hard: list(map[string]string{"workspaces": "20"}),
			},
			wantStatus:  corev1.ConditionFalse,
			wantReason:  corev1alpha1.ShardReasonInvalidResourceLimits,
			wantMessage: "soft/hard: workspaces=20/20; soft workspaces=20 is not below hard workspaces=20, so the shard is never deprioritized before it refuses new workspaces",
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			c := &Controller{}
			shard := &corev1alpha1.Shard{
				ObjectMeta: metav1.ObjectMeta{Name: "root"},
				Spec:       corev1alpha1.ShardSpec{ResourceLimits: scenario.limits},
			}
			if err := c.reconcile(context.Background(), shard); err != nil {
				t.Fatal(err)
			}

			cond := conditions.Get(shard, corev1alpha1.ShardResourceLimitsApplied)
			if cond == nil {
				t.Fatal("expected the ResourceLimitsApplied condition to be set")
			}
			if cond.Status != scenario.wantStatus {
				t.Errorf("expected ResourceLimitsApplied=%s, got %s", scenario.wantStatus, cond.Status)
			}
			if cond.Reason != scenario.wantReason {
				t.Errorf("expected reason %q, got %q", scenario.wantReason, cond.Reason)
			}
			if cond.Message != scenario.wantMessage {
				t.Errorf("unexpected message:\n  got  %q\n  want %q", cond.Message, scenario.wantMessage)
			}
		})
	}
}

// TestReconcileResourceLimitsMessageIsParsable pins the contract documented on
// ShardResourceLimitsApplied: consumers parse the message to learn the limits
// in force, so the shape must not drift.
func TestReconcileResourceLimitsMessageIsParsable(t *testing.T) {
	t.Parallel()
	c := &Controller{}
	shard := &corev1alpha1.Shard{
		ObjectMeta: metav1.ObjectMeta{Name: "root"},
		Spec: corev1alpha1.ShardSpec{ResourceLimits: &corev1alpha1.ShardResourceLimits{
			Soft: corev1.ResourceList{corev1alpha1.ResourceWorkspaces: resource.MustParse("10")},
			Hard: corev1.ResourceList{corev1alpha1.ResourceWorkspaces: resource.MustParse("20")},
		}},
	}
	if err := c.reconcile(context.Background(), shard); err != nil {
		t.Fatal(err)
	}

	message := conditions.Get(shard, corev1alpha1.ShardResourceLimitsApplied).Message
	limits, ok := strings.CutPrefix(message, "soft/hard: ")
	if !ok {
		t.Fatalf("expected a %q prefix, got %q", "soft/hard: ", message)
	}
	parsed := map[string][2]string{}
	for _, entry := range strings.Split(limits, ", ") {
		name, tiers, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("entry %q is not <resource>=<soft>/<hard>", entry)
		}
		soft, hard, ok := strings.Cut(tiers, "/")
		if !ok {
			t.Fatalf("entry %q does not carry both tiers", entry)
		}
		parsed[name] = [2]string{soft, hard}
	}
	if diff := cmp.Diff(map[string][2]string{"workspaces": {"10", "20"}}, parsed); diff != "" {
		t.Errorf("unexpected parse of %q (-want +got):\n%s", message, diff)
	}
}
