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

package admissionwebhook

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"

	webhookserver "github.com/kcp-dev/kcp/test/e2e/fixtures/webhook"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/kcp/test/server/pki"
)

// TestGlobalValidatingWebhook exercises the apis.kcp.io/GlobalValidatingWebhook
// admission plugin: a SINGLE ValidatingWebhookConfiguration supplied to the shard
// at startup (--admission-control-config-file) that fires for requests in EVERY
// logical cluster - here on ConfigMaps, a NATIVE resource that is part of no
// APIExport (the case a per-workspace/per-export webhook can't cover globally).
//
// It asserts the three properties that make this webhook distinct:
//   - GLOBAL: one startup config fires in two independent workspaces.
//   - CLUSTER-AWARE: the reviewed object carries each request's own
//     kcp.io/cluster annotation.
//   - DENY + CONTENT: it decodes the full object body and can reject the request.
func TestGlobalValidatingWebhook(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Decoder for AdmissionReview + ConfigMap (the fixture decodes both).
	scheme := runtime.NewScheme()
	require.NoError(t, admissionv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	deserializer := serializer.NewCodecFactory(scheme).UniversalDeserializer()

	var (
		seen sync.Map    // cluster name -> struct{}: which clusters the webhook saw
		deny atomic.Bool // flip to make the webhook reject
	)
	testWebhook := &webhookserver.AdmissionWebhookServer{
		ObjectGVK:    schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"},
		Deserializer: deserializer,
		ResponseFn: func(obj runtime.Object, review *admissionv1.AdmissionReview) (*admissionv1.AdmissionResponse, error) {
			cm := obj.(*corev1.ConfigMap)
			seen.Store(logicalcluster.From(cm).String(), struct{}{})
			if deny.Load() {
				return &admissionv1.AdmissionResponse{
					Allowed: false,
					Result:  &metav1.Status{Message: "denied by e2e global webhook"},
				}, nil
			}
			return &admissionv1.AdmissionResponse{Allowed: true}, nil
		},
	}

	// Serving cert for localhost, generated BEFORE kcp so its CA can be baked into
	// the startup admission config.
	certDir := t.TempDir()
	certFile := filepath.Join(certDir, "webhook.crt")
	keyFile := filepath.Join(certDir, "webhook.key")
	caFile := filepath.Join(certDir, "webhook-ca.crt")
	require.NoError(t, pki.GenerateSelfSignedCert(certFile, keyFile, caFile, []string{"localhost", "127.0.0.1"}))

	port, err := kcptestingserver.GetFreePort(t)
	require.NoError(t, err)
	testWebhook.StartTLS(t, certFile, keyFile, "https://localhost:"+port, port)

	caPEM, err := os.ReadFile(caFile)
	require.NoError(t, err)

	// The global ValidatingWebhookConfiguration + the AdmissionConfiguration that
	// points our plugin at it. objectSelector keeps the blast radius to this
	// test's ConfigMaps only (so kcp's own ConfigMaps are never intercepted).
	vwcPath := filepath.Join(certDir, "global-vwc.yaml")
	vwc := fmt.Sprintf(`apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: e2e-global
webhooks:
- name: e2e.globalwebhook.kcp.io
  clientConfig:
    url: %q
    caBundle: %q
  rules:
  - apiGroups: [""]
    apiVersions: ["v1"]
    operations: ["CREATE"]
    resources: ["configmaps"]
    scope: "*"
  objectSelector:
    matchLabels:
      e2e-globalwebhook: "true"
  failurePolicy: Fail
  timeoutSeconds: 5
  sideEffects: None
  admissionReviewVersions: ["v1"]
`, testWebhook.GetURL(), base64.StdEncoding.EncodeToString(caPEM))
	require.NoError(t, os.WriteFile(vwcPath, []byte(vwc), 0o644))

	admissionCfgPath := filepath.Join(certDir, "admission-config.yaml")
	admissionCfg := fmt.Sprintf(`apiVersion: apiserver.config.k8s.io/v1
kind: AdmissionConfiguration
plugins:
- name: apis.kcp.io/GlobalValidatingWebhook
  path: %q
`, vwcPath)
	require.NoError(t, os.WriteFile(admissionCfgPath, []byte(admissionCfg), 0o644))

	// A dedicated shard started with the global webhook wired in.
	server := kcptesting.PrivateKcpServer(t,
		kcptestingserver.WithCustomArguments("--admission-control-config-file", admissionCfgPath))
	// Avoid the known early-shutdown race (see authorizer e2e).
	time.Sleep(3 * time.Second)

	cfg := server.BaseConfig(t)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	// Two independent workspaces; one startup webhook must fire in both.
	ws1Path, ws1 := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())
	ws2Path, ws2 := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())

	newCM := func(name string) *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"e2e-globalwebhook": "true"}},
		}
	}

	t.Log("Create a labeled ConfigMap in each workspace; the single global webhook fires in both.")
	for i, wsPath := range []logicalcluster.Path{ws1Path, ws2Path} {
		name := fmt.Sprintf("allowed-%d", i)
		var lastErr error
		ok := func() bool {
			for start := time.Now(); time.Since(start) < 30*time.Second; time.Sleep(200 * time.Millisecond) {
				_, lastErr = kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(ctx, newCM(name), metav1.CreateOptions{})
				if lastErr == nil {
					return true
				}
			}
			return false
		}()
		require.True(t, ok, "creating ConfigMap in %s failed; last error: %v", wsPath, lastErr)
	}

	// Cluster-aware: the webhook saw each workspace's own kcp.io/cluster.
	_, saw1 := seen.Load(ws1.Spec.Cluster)
	_, saw2 := seen.Load(ws2.Spec.Cluster)
	require.True(t, saw1, "webhook should have seen ws1 cluster %q (stamped kcp.io/cluster)", ws1.Spec.Cluster)
	require.True(t, saw2, "webhook should have seen ws2 cluster %q (stamped kcp.io/cluster)", ws2.Spec.Cluster)

	t.Log("Flip the webhook to deny; a matching ConfigMap create is now rejected.")
	deny.Store(true)
	_, err = kubeClusterClient.Cluster(ws1Path).CoreV1().ConfigMaps("default").Create(ctx, newCM("denied"), metav1.CreateOptions{})
	require.Error(t, err, "create should be denied by the global webhook")
	require.Contains(t, err.Error(), "denied by e2e global webhook")
}
