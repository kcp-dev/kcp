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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/index/indextest"
	"github.com/kcp-dev/kcp/pkg/shardlookup"
)

// noCache expires every cached lookup at once, for tests that count the
// requests reaching the shards.
var noCache = shardlookup.TTLOptions{SuccessTTL: time.Nanosecond, FailureTTL: time.Nanosecond}

// fakeShard is a shard answering the lookups the authenticator makes for
// the service accounts and token secrets it holds, in any logical cluster.
type fakeShard struct {
	*httptest.Server
	requests atomic.Int32

	mu       sync.Mutex
	accounts map[string]*corev1.ServiceAccount // by namespace/name
	secrets  map[string]*corev1.Secret         // by namespace/name
}

func newFakeShard(t *testing.T) *fakeShard {
	t.Helper()
	s := &fakeShard{
		accounts: map[string]*corev1.ServiceAccount{},
		secrets:  map[string]*corev1.Secret{},
	}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		w.Header().Set("Content-Type", "application/json")

		// /clusters/<cluster>/api/v1/namespaces/<ns>/<resource>/<name>
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if len(parts) != 8 || parts[0] != "clusters" || parts[2] != "api" || parts[3] != "v1" || parts[4] != "namespaces" {
			notFound(w)
			return
		}
		key := parts[5] + "/" + parts[7]

		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && parts[6] == "serviceaccounts":
			if sa, ok := s.accounts[key]; ok {
				_ = json.NewEncoder(w).Encode(sa)
				return
			}
		case r.Method == http.MethodGet && parts[6] == "secrets":
			if secret, ok := s.secrets[key]; ok {
				_ = json.NewEncoder(w).Encode(secret)
				return
			}
		case r.Method == http.MethodPatch && parts[6] == "secrets":
			if secret, ok := s.secrets[key]; ok {
				patched := secret.DeepCopy()
				if patched.Annotations == nil {
					patched.Annotations = map[string]string{}
				}
				patched.Annotations["patched"] = "true"
				_ = json.NewEncoder(w).Encode(patched)
				return
			}
		}
		notFound(w)
	}))
	t.Cleanup(s.Close)
	return s
}

func notFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`))
}

func (s *fakeShard) add(sa *corev1.ServiceAccount, secret *corev1.Secret) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sa != nil {
		s.accounts[sa.Namespace+"/"+sa.Name] = sa
	}
	if secret != nil {
		s.secrets[secret.Namespace+"/"+secret.Name] = secret
	}
}

// cachedShards returns the names of the shards the lookup holds clients for.
func (s *ShardRoutedServiceAccounts) cachedShards() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.clients))
	for name := range s.clients {
		names = append(names, name)
	}
	return names
}

// TestShardRoutedServiceAccounts walks the lookup through its states in
// order: before the index exists, with a known cluster, with an unknown one.
func TestShardRoutedServiceAccounts(t *testing.T) {
	t.Parallel()

	shard := newFakeShard(t)
	shard.add(
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "sa", Namespace: "default", UID: "sa-uid"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token", Namespace: "default"}},
	)

	config := &rest.Config{Host: "https://kcp.io/fake", TLSClientConfig: rest.TLSClientConfig{Insecure: true}}
	lookup := NewShardRoutedServiceAccounts(config)
	ctx := context.Background()

	t.Log("fails closed before the index exists")
	_, err := lookup.Cluster("ws1").GetServiceAccount(ctx, "default", "sa")
	require.True(t, apierrors.IsServiceUnavailable(err), "got %v", err)

	lookup.SetIndex(&indextest.Index{
		Clusters: map[logicalcluster.Name]string{"ws1": "shard-a"},
		Shards:   map[string]string{"shard-a": shard.URL},
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
	require.Positive(t, shard.requests.Load(), "the shard must have been asked")

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

// TestShardRoutedServiceAccountsClientCache checks that the per-shard
// clients follow the shard set: one client per shard, replaced when the
// shard moves, dropped when the shard is gone.
func TestShardRoutedServiceAccountsClientCache(t *testing.T) {
	t.Parallel()

	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "sa", Namespace: "default", UID: "sa-uid"}}
	shardA, shardB, shardC := newFakeShard(t), newFakeShard(t), newFakeShard(t)
	for _, s := range []*fakeShard{shardA, shardB, shardC} {
		s.add(sa, nil)
	}

	config := &rest.Config{Host: "https://kcp.io/fake", TLSClientConfig: rest.TLSClientConfig{Insecure: true}}
	lookup := newShardRoutedServiceAccounts(config, noCache)
	ctx := context.Background()

	lookup.SetIndex(&indextest.Index{
		Clusters: map[logicalcluster.Name]string{"ws1": "shard-a", "ws2": "shard-b"},
		Shards:   map[string]string{"shard-a": shardA.URL, "shard-b": shardB.URL},
	})

	t.Log("one client per shard, reused across lookups")
	for range 3 {
		_, err := lookup.Cluster("ws1").GetServiceAccount(ctx, "default", "sa")
		require.NoError(t, err)
	}
	_, err := lookup.Cluster("ws2").GetServiceAccount(ctx, "default", "sa")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"shard-a", "shard-b"}, lookup.cachedShards())
	require.EqualValues(t, 3, shardA.requests.Load())
	require.EqualValues(t, 1, shardB.requests.Load())

	t.Log("a shard the index forgot loses its client once another shard is looked up afresh")
	lookup.SetIndex(&indextest.Index{
		Clusters: map[logicalcluster.Name]string{"ws2": "shard-b", "ws3": "shard-c"},
		Shards:   map[string]string{"shard-b": shardB.URL, "shard-c": shardC.URL},
	})
	_, err = lookup.Cluster("ws2").GetServiceAccount(ctx, "default", "sa")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"shard-a", "shard-b"}, lookup.cachedShards(), "a cache hit does not prune")
	_, err = lookup.Cluster("ws3").GetServiceAccount(ctx, "default", "sa")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"shard-b", "shard-c"}, lookup.cachedShards(), "the client of the forgotten shard is dropped")
	require.EqualValues(t, 1, shardC.requests.Load())

	t.Log("a shard that moved to a new URL gets a new client")
	lookup.SetIndex(&indextest.Index{
		Clusters: map[logicalcluster.Name]string{"ws2": "shard-b", "ws3": "shard-c"},
		Shards:   map[string]string{"shard-b": shardA.URL, "shard-c": shardC.URL},
	})
	before := shardA.requests.Load()
	_, err = lookup.Cluster("ws2").GetServiceAccount(ctx, "default", "sa")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"shard-b", "shard-c"}, lookup.cachedShards())
	require.Equal(t, before+1, shardA.requests.Load(), "the request went to the shard's new URL")
	require.EqualValues(t, 2, shardB.requests.Load(), "the old URL is not asked any more")
}

// TestShardRoutedServiceAccountsResultCache checks that service accounts and
// secrets are cached per logical cluster, failed lookups included, and that
// index misses are not.
func TestShardRoutedServiceAccountsResultCache(t *testing.T) {
	t.Parallel()

	shard := newFakeShard(t)
	shard.add(
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "sa", Namespace: "default", UID: "sa-uid"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token", Namespace: "default"}},
	)

	config := &rest.Config{Host: "https://kcp.io/fake", TLSClientConfig: rest.TLSClientConfig{Insecure: true}}
	lookup := newShardRoutedServiceAccounts(config, shardlookup.TTLOptions{SuccessTTL: time.Hour, FailureTTL: time.Hour})
	ctx := context.Background()

	index := &indextest.Index{
		Clusters: map[logicalcluster.Name]string{"ws1": "shard-a", "ws2": "shard-a"},
		Shards:   map[string]string{"shard-a": shard.URL},
	}
	lookup.SetIndex(index)

	t.Log("repeated lookups of a service account and a secret ask the shard once each")
	for range 3 {
		_, err := lookup.Cluster("ws1").GetServiceAccount(ctx, "default", "sa")
		require.NoError(t, err)
		_, err = lookup.Cluster("ws1").GetSecret(ctx, "default", "token")
		require.NoError(t, err)
	}
	require.EqualValues(t, 2, shard.requests.Load())

	t.Log("the same name in another logical cluster is another entry")
	_, err := lookup.Cluster("ws2").GetServiceAccount(ctx, "default", "sa")
	require.NoError(t, err)
	require.EqualValues(t, 3, shard.requests.Load())

	t.Log("a service account the shard does not have is cached as not found")
	for range 2 {
		_, err = lookup.Cluster("ws1").GetServiceAccount(ctx, "default", "missing")
		require.True(t, apierrors.IsNotFound(err), "got %v", err)
	}
	require.EqualValues(t, 4, shard.requests.Load())

	t.Log("an index miss is not cached: the cluster is found once the index knows it")
	_, err = lookup.Cluster("ws3").GetServiceAccount(ctx, "default", "sa")
	require.True(t, apierrors.IsNotFound(err), "got %v", err)
	lookup.SetIndex(&indextest.Index{
		Clusters: map[logicalcluster.Name]string{"ws3": "shard-a"},
		Shards:   map[string]string{"shard-a": shard.URL},
	})
	_, err = lookup.Cluster("ws3").GetServiceAccount(ctx, "default", "sa")
	require.NoError(t, err)
	require.EqualValues(t, 5, shard.requests.Load())

	t.Log("a cancelled request context does not fail the shared lookup")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = lookup.Cluster("ws3").GetSecret(cancelled, "default", "token")
	require.NoError(t, err)
	require.EqualValues(t, 6, shard.requests.Load())
}
