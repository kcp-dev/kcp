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

package globalwebhook

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
)

const twoWebhookConfig = `
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: metering
webhooks:
- name: enforce.metering.contrib.kcp.io
  clientConfig:
    url: https://metering-webhook.metering.svc/validate
    caBundle: dGVzdA==
  rules:
  - apiGroups: ["*"]
    apiVersions: ["*"]
    operations: ["CREATE","DELETE"]
    resources: ["*"]
  failurePolicy: Ignore
  sideEffects: None
  admissionReviewVersions: ["v1"]
- name: second.metering.contrib.kcp.io
  clientConfig:
    url: https://metering-webhook.metering.svc/validate2
    caBundle: dGVzdA==
  sideEffects: None
  admissionReviewVersions: ["v1"]
`

func TestInertWhenUnconfigured(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		config string
		nilCfg bool
	}{
		{name: "nil config", nilCfg: true},
		{name: "empty config", config: ""},
		{name: "zero webhooks", config: "apiVersion: admissionregistration.k8s.io/v1\nkind: ValidatingWebhookConfiguration\nmetadata:\n  name: x\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var r *strings.Reader
			if !tc.nilCfg {
				r = strings.NewReader(tc.config)
			}
			var p *Plugin
			var err error
			if r == nil {
				p, err = NewGlobalValidatingWebhook(nil)
			} else {
				p, err = NewGlobalValidatingWebhook(r)
			}
			if err != nil {
				t.Fatalf("construct: %v", err)
			}
			if p.source != nil {
				t.Fatalf("expected inert plugin (nil source), got a source")
			}
			// Inert plugin admits everything without touching injected deps.
			if err := p.Validate(context.Background(), nil, nil); err != nil {
				t.Fatalf("inert Validate should no-op, got %v", err)
			}
			if err := p.ValidateInitialization(); err != nil {
				t.Fatalf("inert ValidateInitialization should pass, got %v", err)
			}
		})
	}
}

func TestBuildsStaticSourceFromConfig(t *testing.T) {
	t.Parallel()
	p, err := NewGlobalValidatingWebhook(strings.NewReader(twoWebhookConfig))
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if p.source == nil {
		t.Fatal("expected a static source, got nil")
	}
	hooks := p.source.Webhooks()
	if len(hooks) != 2 {
		t.Fatalf("expected 2 webhooks, got %d", len(hooks))
	}
	if !p.source.HasSynced() {
		t.Fatal("static source should always report HasSynced=true")
	}
}

func TestHandlesAllOperations(t *testing.T) {
	t.Parallel()
	p, _ := NewGlobalValidatingWebhook(nil)
	for _, op := range []admission.Operation{admission.Create, admission.Update, admission.Delete, admission.Connect} {
		if !p.Handles(op) {
			t.Errorf("expected plugin to handle %v", op)
		}
	}
}

func TestBadConfigErrors(t *testing.T) {
	t.Parallel()
	if _, err := NewGlobalValidatingWebhook(strings.NewReader("webhooks: [ this: is: not: valid")); err == nil {
		t.Fatal("expected an error for malformed config")
	}
}

func TestStampAnnotationsAndUndo(t *testing.T) {
	t.Parallel()
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{"keep": "me", logicalcluster.AnnotationKey: "stale"},
	}}
	undo := stampAnnotations(cm, map[string]string{
		logicalcluster.AnnotationKey:         "abc123",
		core.LogicalClusterPathAnnotationKey: "root:org:team",
	})
	if got := cm.Annotations[logicalcluster.AnnotationKey]; got != "abc123" {
		t.Errorf("cluster annotation: got %q, want %q", got, "abc123")
	}
	if got := cm.Annotations[core.LogicalClusterPathAnnotationKey]; got != "root:org:team" {
		t.Errorf("path annotation: got %q, want %q", got, "root:org:team")
	}
	undo()
	if got := cm.Annotations[logicalcluster.AnnotationKey]; got != "stale" {
		t.Errorf("after undo cluster annotation: got %q, want %q", got, "stale")
	}
	if _, ok := cm.Annotations[core.LogicalClusterPathAnnotationKey]; ok {
		t.Error("after undo path annotation should be removed")
	}
	if got := cm.Annotations["keep"]; got != "me" {
		t.Errorf("unrelated annotation lost: got %q", got)
	}

	// nil annotations: stamp then undo leaves nil.
	empty := &corev1.ConfigMap{}
	stampAnnotations(empty, map[string]string{logicalcluster.AnnotationKey: "x"})()
	if empty.Annotations != nil {
		t.Errorf("expected nil annotations after undo, got %v", empty.Annotations)
	}
}

func TestClusterPath(t *testing.T) {
	t.Parallel()
	name := logicalcluster.Name("abc123")
	for _, tc := range []struct {
		name string
		get  func(logicalcluster.Name) (*corev1alpha1.LogicalCluster, error)
		want string
	}{
		{name: "no lister", get: nil, want: ""},
		{name: "lookup error falls back to name", get: func(logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
			return nil, errors.New("not found")
		}, want: "abc123"},
		{name: "path annotation", get: func(logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
			return &corev1alpha1.LogicalCluster{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				core.LogicalClusterPathAnnotationKey: "root:org:team",
				logicalcluster.AnnotationKey:         "abc123",
			}}}, nil
		}, want: "root:org:team"},
		{name: "no path annotation falls back to cluster annotation", get: func(logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
			return &corev1alpha1.LogicalCluster{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				logicalcluster.AnnotationKey: "abc123",
			}}}, nil
		}, want: "abc123"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := &Plugin{getLogicalCluster: tc.get}
			if got := p.clusterPath(name); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
