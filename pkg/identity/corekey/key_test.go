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

package corekey

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/identity"
)

var hexSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func pkcs8PEM(t *testing.T, priv any) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func TestSameKeyDifferentFormats(t *testing.T) {
	t.Parallel()

	t.Run("ecdsa", func(t *testing.T) {
		t.Parallel()
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)

		sec1, err := x509.MarshalECPrivateKey(priv)
		require.NoError(t, err)

		fromSEC1, err := Parse(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec1}))
		require.NoError(t, err)
		fromPKCS8, err := Parse(pkcs8PEM(t, priv))
		require.NoError(t, err)

		require.Equal(t, fromSEC1.ID(), fromPKCS8.ID())
		h1, err := fromSEC1.IdentityHash("tenancy.kcp.io")
		require.NoError(t, err)
		h2, err := fromPKCS8.IdentityHash("tenancy.kcp.io")
		require.NoError(t, err)
		require.Equal(t, h1, h2)
	})

	t.Run("rsa", func(t *testing.T) {
		t.Parallel()
		priv, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)

		pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})

		fromPKCS1, err := Parse(pkcs1)
		require.NoError(t, err)
		fromPKCS8, err := Parse(pkcs8PEM(t, priv))
		require.NoError(t, err)

		require.Equal(t, fromPKCS1.ID(), fromPKCS8.ID())
		h1, err := fromPKCS1.IdentityHash("topology.kcp.io")
		require.NoError(t, err)
		h2, err := fromPKCS8.IdentityHash("topology.kcp.io")
		require.NoError(t, err)
		require.Equal(t, h1, h2)
	})

	t.Run("ed25519", func(t *testing.T) {
		t.Parallel()
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		k, err := Parse(pkcs8PEM(t, priv))
		require.NoError(t, err)
		require.Regexp(t, hexSHA256, k.ID())
	})
}

func TestCertificateBlocksAreSkipped(t *testing.T) {
	t.Parallel()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	// a cert-manager Secret's tls.key only holds the key, but a combined
	// file must work as well.
	combined := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not-a-cert")}), pkcs8PEM(t, priv)...)
	k, err := Parse(combined)
	require.NoError(t, err)
	want, err := FromPrivateKey(priv)
	require.NoError(t, err)
	require.Equal(t, want.ID(), k.ID())
}

func TestIdentitiesAreDeterministicAndDistinct(t *testing.T) {
	t.Parallel()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	k, err := FromPrivateKey(priv)
	require.NoError(t, err)

	seen := map[string]string{}
	for _, name := range []string{"tenancy.kcp.io", "topology.kcp.io", "shards.core.kcp.io", "migration.kcp.io", "cache.kcp.io"} {
		h, err := k.IdentityHash(name)
		require.NoError(t, err)
		require.Regexp(t, hexSHA256, h)
		again, err := k.IdentityHash(name)
		require.NoError(t, err)
		require.Equal(t, h, again, "derivation must be deterministic")
		for other, otherHash := range seen {
			require.NotEqual(t, otherHash, h, "%s and %s must have different identities", other, name)
		}
		seen[name] = h
	}

	// a different key gives different identities
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	k2, err := FromPrivateKey(other)
	require.NoError(t, err)
	require.NotEqual(t, k.ID(), k2.ID())
	h2, err := k2.IdentityHash("tenancy.kcp.io")
	require.NoError(t, err)
	require.NotEqual(t, seen["tenancy.kcp.io"], h2)
}

// TestHashMatchesIdentitySecret checks that an identity Secret created from
// the derived identity key hashes to the derived identity hash, i.e. the
// secrets the root shard seeds and the hashes the other shards compute agree.
func TestHashMatchesIdentitySecret(t *testing.T) {
	t.Parallel()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	k, err := FromPrivateKey(priv)
	require.NoError(t, err)

	identityKey, err := k.IdentityKey("tenancy.kcp.io")
	require.NoError(t, err)
	hash, err := k.IdentityHash("tenancy.kcp.io")
	require.NoError(t, err)

	secret := &corev1.Secret{Data: map[string][]byte{apisv1alpha1.SecretKeyAPIExportIdentity: []byte(identityKey)}}
	got, err := identity.IdentityHash(secret)
	require.NoError(t, err)
	require.Equal(t, hash, got)

	sum := sha256.Sum256([]byte(identityKey))
	require.Equal(t, hex.EncodeToString(sum[:]), got)
}

func TestRejects(t *testing.T) {
	t.Parallel()

	t.Run("short rsa", func(t *testing.T) {
		t.Parallel()
		priv, err := rsa.GenerateKey(rand.Reader, 1024) //nolint:gosec // deliberately too short, must be rejected
		require.NoError(t, err)
		_, err = Parse(pkcs8PEM(t, priv))
		require.ErrorContains(t, err, "1024 bits")
	})

	t.Run("no key", func(t *testing.T) {
		t.Parallel()
		_, err := Parse([]byte("garbage"))
		require.ErrorContains(t, err, "no PEM-encoded private key")
	})

	t.Run("empty export name", func(t *testing.T) {
		t.Parallel()
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		k, err := FromPrivateKey(priv)
		require.NoError(t, err)
		_, err = k.IdentityHash("")
		require.Error(t, err)
	})
}

func TestLoad(t *testing.T) {
	t.Parallel()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "tls.key")
	require.NoError(t, os.WriteFile(path, pkcs8PEM(t, priv), 0o600))

	k, err := Load(path)
	require.NoError(t, err)
	want, err := FromPrivateKey(priv)
	require.NoError(t, err)
	require.Equal(t, want.ID(), k.ID())

	_, err = Load(filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
}
