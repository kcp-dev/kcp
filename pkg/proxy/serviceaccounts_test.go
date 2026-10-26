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

package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/logicalcluster/v3"

	kcpindex "github.com/kcp-dev/kcp/pkg/index"
)

// fakeIndex maps logical clusters to shards and shards to base URLs.
type fakeIndex struct {
	shards map[logicalcluster.Name]string
	urls   map[string]string
}

func (f fakeIndex) Lookup(path logicalcluster.Path) (kcpindex.Result, bool) {
	name, ok := path.Name()
	if !ok {
		return kcpindex.Result{}, false
	}
	shard, ok := f.shards[name]
	if !ok {
		return kcpindex.Result{}, false
	}
	return kcpindex.Result{Shard: shard, Cluster: name, URL: f.urls[shard]}, true
}

func (f fakeIndex) BaseURL(shard string) (string, bool) {
	url, ok := f.urls[shard]
	return url, ok
}

// TestShardRoutedServiceAccounts walks the lookup through its states in
// order: before the index exists, with a known cluster, with an unknown one.
func TestShardRoutedServiceAccounts(t *testing.T) {
	t.Parallel()

	// a fake shard answering the lookups the authenticator makes
	var requests atomic.Int32
	shard := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/clusters/ws1/api/v1/namespaces/default/serviceaccounts/sa":
			_ = json.NewEncoder(w).Encode(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "sa", Namespace: "default", UID: "sa-uid"}})
		case r.Method == http.MethodGet && r.URL.Path == "/clusters/ws1/api/v1/namespaces/default/secrets/token":
			_ = json.NewEncoder(w).Encode(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token", Namespace: "default"}})
		case r.Method == http.MethodPatch && r.URL.Path == "/clusters/ws1/api/v1/namespaces/default/secrets/token":
			_ = json.NewEncoder(w).Encode(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token", Namespace: "default", Annotations: map[string]string{"patched": "true"}}})
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`))
		}
	}))
	t.Cleanup(shard.Close)

	config := &rest.Config{Host: "https://kcp.io/fake", TLSClientConfig: rest.TLSClientConfig{Insecure: true}}
	lookup := NewShardRoutedServiceAccounts(config)
	ctx := context.Background()

	t.Log("fails closed before the index exists")
	_, err := lookup.Cluster("ws1").GetServiceAccount(ctx, "default", "sa")
	require.True(t, apierrors.IsServiceUnavailable(err), "got %v", err)

	lookup.SetIndex(fakeIndex{
		shards: map[logicalcluster.Name]string{"ws1": "shard-a"},
		urls:   map[string]string{"shard-a": shard.URL},
	})

	t.Log("looks the service account and secret up on the hosting shard")
	sa, err := lookup.Cluster("ws1").GetServiceAccount(ctx, "default", "sa")
	require.NoError(t, err)
	require.Equal(t, types.UID("sa-uid"), sa.UID)
	secret, err := lookup.Cluster("ws1").GetSecret(ctx, "default", "token")
	require.NoError(t, err)
	require.Equal(t, "token", secret.Name)

	t.Log("writes the token secret on the hosting shard")
	secret, err = lookup.SecretsWriter().Cluster(logicalcluster.NewPath("ws1")).Namespace("default").Patch(ctx, "token", types.MergePatchType, []byte(`{}`), metav1.PatchOptions{})
	require.NoError(t, err)
	require.Equal(t, "true", secret.Annotations["patched"])
	require.Positive(t, requests.Load(), "the shard must have been asked")

	t.Log("a cluster the index does not know is not found")
	_, err = lookup.Cluster("unknown").GetServiceAccount(ctx, "default", "sa")
	require.True(t, apierrors.IsNotFound(err), "got %v", err)
	_, err = lookup.SecretsWriter().Cluster(logicalcluster.NewPath("unknown")).Namespace("default").Get(ctx, "token", metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err), "got %v", err)

	t.Log("pods and nodes do not exist in kcp")
	_, err = lookup.Cluster("ws1").GetPod(ctx, "default", "pod")
	require.True(t, apierrors.IsNotFound(err))
	_, err = lookup.Cluster("ws1").GetNode(ctx, "node")
	require.True(t, apierrors.IsNotFound(err))

	t.Log("listing across clusters is not supported")
	_, err = lookup.SecretsWriter().List(ctx, metav1.ListOptions{})
	require.True(t, apierrors.IsMethodNotSupported(err), "got %v", err)
}
