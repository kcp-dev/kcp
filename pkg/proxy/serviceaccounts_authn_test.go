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
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/keyutil"
	"k8s.io/kubernetes/pkg/apis/core"
	"k8s.io/kubernetes/pkg/serviceaccount"

	"github.com/kcp-dev/logicalcluster/v3"

	proxyoptions "github.com/kcp-dev/kcp/pkg/proxy/options"
)

// TestServiceAccountAuthenticationThroughShards builds the proxy's
// authenticator the way NewConfig does, with service account authentication
// enabled and the shard-routed lookup behind it, and sends signed legacy and
// bound tokens of service accounts living on two different shards through
// it. It pins the wiring of ServiceAccountTokenGetter and SecretsWriter: a
// token is accepted only if its service account and secret are found on the
// shard hosting its logical cluster, and never through any other shard.
func TestServiceAccountAuthenticationThroughShards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// the signing key; the proxy only gets the public half
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pem, err := keyutil.MarshalPrivateKeyToPEM(key)
	require.NoError(t, err)
	keyFile := filepath.Join(t.TempDir(), "sa.key")
	require.NoError(t, os.WriteFile(keyFile, pem, 0600))

	// two shards, each hosting one logical cluster with one service account
	type account struct {
		cluster logicalcluster.Name
		shard   *fakeShard
		sa      *corev1.ServiceAccount
		secret  *corev1.Secret
	}
	accounts := map[string]*account{
		"shard-a": {cluster: "ws1", shard: newFakeShard(t)},
		"shard-b": {cluster: "ws2", shard: newFakeShard(t)},
	}
	for name, a := range accounts {
		a.sa = &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
			Name: "sa", Namespace: "default", UID: types.UID("uid-" + name),
			Annotations: map[string]string{logicalcluster.AnnotationKey: a.cluster.String()},
		}}
		a.secret = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: "sa-token", Namespace: "default", UID: types.UID("secret-uid-" + name),
			Annotations: map[string]string{logicalcluster.AnnotationKey: a.cluster.String()},
		}, Data: map[string][]byte{}}
		a.shard.add(a.sa, a.secret)
	}
	index := fakeIndex{shards: map[logicalcluster.Name]string{}, urls: map[string]string{}}
	for name, a := range accounts {
		index.shards[a.cluster] = name
		index.urls[name] = a.shard.URL
	}

	lookup := NewShardRoutedServiceAccounts(&rest.Config{Host: "https://kcp.io/fake", TLSClientConfig: rest.TLSClientConfig{Insecure: true}})
	lookup.SetIndex(index)

	options := proxyoptions.NewAuthentication()
	options.BuiltInOptions.ServiceAccounts.KeyFiles = []string{keyFile}
	var info genericapiserver.AuthenticationInfo
	require.NoError(t, options.ApplyTo(ctx, &info, nil, lookup))
	require.NotNil(t, info.Authenticator)

	issuer := options.BuiltInOptions.ServiceAccounts.Issuers[0]
	legacy, err := serviceaccount.JWTTokenGenerator(serviceaccount.LegacyIssuer, key)
	require.NoError(t, err)
	bound, err := serviceaccount.JWTTokenGenerator(issuer, key)
	require.NoError(t, err)

	legacyToken := func(a *account) string {
		claims, private := serviceaccount.LegacyClaims(*a.sa, *a.secret)
		token, err := legacy.GenerateToken(ctx, claims, private)
		require.NoError(t, err)
		// the legacy validator compares the token with the secret's copy
		a.shard.mu.Lock()
		a.secret.Data[corev1.ServiceAccountTokenKey] = []byte(token)
		a.shard.mu.Unlock()
		return token
	}
	boundToken := func(a *account) string {
		sa := core.ServiceAccount{ObjectMeta: a.sa.ObjectMeta}
		secret := core.Secret{ObjectMeta: a.secret.ObjectMeta}
		claims, private, err := serviceaccount.Claims(sa, nil, &secret, nil, nil, nil, 3600, 0, []string{issuer}, nil)
		require.NoError(t, err)
		token, err := bound.GenerateToken(ctx, claims, private)
		require.NoError(t, err)
		return token
	}
	authenticate := func(token string) (string, bool) {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "https://proxy/clusters/anything/api", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, ok, err := info.Authenticator.AuthenticateRequest(req)
		if err != nil || !ok {
			return "", false
		}
		return resp.User.GetName(), true
	}

	for shardName, a := range accounts {
		for kind, mint := range map[string]func(*account) string{"legacy": legacyToken, "bound": boundToken} {
			t.Logf("%s token of the service account on %s authenticates through that shard", kind, shardName)
			for _, other := range accounts {
				other.shard.requests.Store(0)
			}
			name, ok := authenticate(mint(a))
			require.True(t, ok, "%s token of %s rejected", kind, shardName)
			require.Equal(t, "system:serviceaccount:default:sa", name)
			require.Positive(t, a.shard.requests.Load(), "the hosting shard must have been asked")
			for otherName, other := range accounts {
				if otherName != shardName {
					require.Zero(t, other.shard.requests.Load(), "%s must not be asked about %s", otherName, shardName)
				}
			}
		}
	}

	t.Log("a token of a service account whose cluster the index does not know is rejected")
	orphan := &account{cluster: "ws-unknown", shard: accounts["shard-a"].shard}
	orphan.sa = accounts["shard-a"].sa.DeepCopy()
	orphan.sa.Annotations[logicalcluster.AnnotationKey] = orphan.cluster.String()
	orphan.secret = accounts["shard-a"].secret.DeepCopy()
	_, ok := authenticate(boundToken(orphan))
	require.False(t, ok)

	t.Log("a token whose service account the hosting shard does not have is rejected")
	stranger := &account{cluster: "ws1", shard: accounts["shard-a"].shard}
	stranger.sa = accounts["shard-a"].sa.DeepCopy()
	stranger.sa.Name = "nobody"
	stranger.secret = accounts["shard-a"].secret.DeepCopy()
	_, ok = authenticate(boundToken(stranger))
	require.False(t, ok)

	t.Log("a token signed by another key is rejected")
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	forged, err := serviceaccount.JWTTokenGenerator(issuer, otherKey)
	require.NoError(t, err)
	a := accounts["shard-a"]
	claims, private, err := serviceaccount.Claims(core.ServiceAccount{ObjectMeta: a.sa.ObjectMeta}, nil, &core.Secret{ObjectMeta: a.secret.ObjectMeta}, nil, nil, nil, 3600, 0, []string{issuer}, nil)
	require.NoError(t, err)
	token, err := forged.GenerateToken(ctx, claims, private)
	require.NoError(t, err)
	_, ok = authenticate(token)
	require.False(t, ok)
}
