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
	"strings"
	"testing"

	"k8s.io/apiserver/pkg/admission"
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
