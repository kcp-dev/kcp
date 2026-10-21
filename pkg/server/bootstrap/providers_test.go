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

package bootstrap

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/logicalcluster/v3"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	kcpfake "github.com/kcp-dev/sdk/client/clientset/versioned/cluster/fake"

	configshard "github.com/kcp-dev/kcp/config/shard"
	"github.com/kcp-dev/kcp/pkg/identity/corekey"
)

func newKey(t *testing.T) *corekey.Key {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	k, err := corekey.FromPrivateKey(priv)
	require.NoError(t, err)
	return k
}

func set(shard string, keyID string, exports map[string]string) *adminv1alpha1.CoreIdentitySet {
	s := &adminv1alpha1.CoreIdentitySet{ObjectMeta: metav1.ObjectMeta{
		Name:        shard,
		Annotations: map[string]string{logicalcluster.AnnotationKey: configshard.SystemShardCluster.String()},
	}}
	s.Status.Shard = shard
	s.Status.KeyID = keyID
	for name, hash := range exports {
		s.Status.Exports = append(s.Status.Exports, adminv1alpha1.CoreIdentity{Name: name, IdentityHash: hash, Source: adminv1alpha1.CoreIdentitySourceLegacy})
	}
	return s
}

func fixed(hash string) Provider {
	return func(context.Context, string) (ResolvedIdentity, error) { return legacy(hash), nil }
}

func failing(msg string) Provider {
	return func(context.Context, string) (ResolvedIdentity, error) { return ResolvedIdentity{}, errors.New(msg) }
}

func TestChain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	id, err := Chain(failing("first"), nil, fixed("a"), fixed("b"))(ctx, "tenancy.kcp.io")
	require.NoError(t, err)
	require.Equal(t, "a", id.Hash, "the first provider with an answer wins")

	_, err = Chain(failing("first"), failing("second"))(ctx, "tenancy.kcp.io")
	require.ErrorContains(t, err, "first")
	require.ErrorContains(t, err, "second")

	_, err = Chain()(ctx, "tenancy.kcp.io")
	require.ErrorIs(t, err, ErrUnknown)
}

func TestIdentityFromSets(t *testing.T) {
	t.Parallel()

	sets := []adminv1alpha1.CoreIdentitySet{
		*set("shard-1", "", map[string]string{"tenancy.kcp.io": "x"}),
		*set("root", "", map[string]string{"tenancy.kcp.io": "r"}),
	}
	id, found, err := IdentityFromSets(sets, "tenancy.kcp.io")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "r", id.Hash, "the root shard wins")

	sets = []adminv1alpha1.CoreIdentitySet{
		*set("shard-1", "", map[string]string{"tenancy.kcp.io": "x"}),
		*set("shard-2", "", map[string]string{"tenancy.kcp.io": "x"}),
	}
	id, found, err = IdentityFromSets(sets, "tenancy.kcp.io")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "x", id.Hash, "agreeing peers are fine")

	sets[1] = *set("shard-2", "", map[string]string{"tenancy.kcp.io": "y"})
	_, _, err = IdentityFromSets(sets, "tenancy.kcp.io")
	require.ErrorContains(t, err, "disagree")

	_, found, err = IdentityFromSets(sets, "topology.kcp.io")
	require.NoError(t, err)
	require.False(t, found)
}

func TestKeyProviderAndGate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	key := newKey(t)
	other := newKey(t)

	require.Nil(t, KeyProvider(nil, nil), "no key, no provider")

	want, err := key.IdentityHash("tenancy.kcp.io")
	require.NoError(t, err)

	t.Run("no local set yet", func(t *testing.T) {
		t.Parallel()
		local := NewLocalCoreIdentitySet(kcpfake.NewClientset(), "shard-1")
		id, err := KeyProvider(key, KeyGate(key, local))(ctx, "tenancy.kcp.io")
		require.NoError(t, err)
		require.Equal(t, ResolvedIdentity{Hash: want, Source: adminv1alpha1.CoreIdentitySourceKey, KeyID: key.ID(), Derivation: corekey.Derivation}, id)
	})

	t.Run("same key recorded", func(t *testing.T) {
		t.Parallel()
		local := NewLocalCoreIdentitySet(kcpfake.NewClientset(set("shard-1", key.ID(), nil)), "shard-1")
		_, err := KeyProvider(key, KeyGate(key, local))(ctx, "tenancy.kcp.io")
		require.NoError(t, err)
	})

	t.Run("key changed", func(t *testing.T) {
		t.Parallel()
		local := NewLocalCoreIdentitySet(kcpfake.NewClientset(set("shard-1", other.ID(), nil)), "shard-1")
		_, err := KeyProvider(key, KeyGate(key, local))(ctx, "tenancy.kcp.io")
		require.ErrorContains(t, err, "not the active key")
	})
}

func TestLocalCoreIdentitySetProvider(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	client := kcpfake.NewClientset(set("shard-1", "k", map[string]string{"tenancy.kcp.io": "a"}))

	byName := NewLocalCoreIdentitySet(client, "shard-1")
	id, err := byName.Provider()(ctx, "tenancy.kcp.io")
	require.NoError(t, err)
	require.Equal(t, "a", id.Hash)
	require.Equal(t, adminv1alpha1.CoreIdentitySourceLegacy, id.Source)
	_, err = byName.Provider()(ctx, "topology.kcp.io")
	require.ErrorIs(t, err, ErrUnknown)
	recorded, err := byName.RecordedKeyID(ctx)
	require.NoError(t, err)
	require.Equal(t, "k", recorded)

	// virtual-workspaces servers do not know their shard name
	anonymous := NewLocalCoreIdentitySet(client, "")
	id, err = anonymous.Provider()(ctx, "tenancy.kcp.io")
	require.NoError(t, err)
	require.Equal(t, "a", id.Hash)

	empty := NewLocalCoreIdentitySet(kcpfake.NewClientset(), "")
	recorded, err = empty.RecordedKeyID(ctx)
	require.NoError(t, err)
	require.Empty(t, recorded)
}

func TestClassifier(t *testing.T) {
	t.Parallel()
	key := newKey(t)
	derived, err := key.IdentityHash("tenancy.kcp.io")
	require.NoError(t, err)

	classify := NewClassifier(key, map[string]string{"topology.kcp.io": "dev"})

	id := classify("tenancy.kcp.io", legacy(derived))
	require.Equal(t, adminv1alpha1.CoreIdentitySourceKey, id.Source)
	require.Equal(t, key.ID(), id.KeyID)
	require.Equal(t, corekey.Derivation, id.Derivation)

	id = classify("topology.kcp.io", legacy("dev"))
	require.Equal(t, adminv1alpha1.CoreIdentitySourceDevFile, id.Source)

	id = classify("topology.kcp.io", legacy("random"))
	require.Equal(t, adminv1alpha1.CoreIdentitySourceLegacy, id.Source)

	// an explicit source is kept
	id = classify("tenancy.kcp.io", ResolvedIdentity{Hash: "x", Source: adminv1alpha1.CoreIdentitySourceDevFile})
	require.Equal(t, adminv1alpha1.CoreIdentitySourceDevFile, id.Source)
}

func TestIdentitiesResolve(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	groups := map[string]string{"tenancy.kcp.io": "tenancy.kcp.io"}
	grs := map[schema.GroupResource]string{
		{Group: "core.kcp.io", Resource: "shards"}:                        "shards.core.kcp.io",
		{Group: "migration.kcp.io", Resource: "logicalclustermigrations"}: "migration.kcp.io",
		{Group: "migration.kcp.io", Resource: "apiexportstoragerekeys"}:   "migration.kcp.io",
	}
	ids := NewIdentities(groups, grs)
	require.Equal(t, []string{"migration.kcp.io", "shards.core.kcp.io", "tenancy.kcp.io"}, ids.ExportNames())
	require.False(t, ids.Resolved())

	// first pass: only tenancy is known
	partial := func(_ context.Context, name string) (ResolvedIdentity, error) {
		if name == "tenancy.kcp.io" {
			return legacy("t"), nil
		}
		return ResolvedIdentity{}, ErrUnknown
	}
	require.Error(t, ids.Resolve(ctx, partial))
	require.Equal(t, map[string]ResolvedIdentity{"tenancy.kcp.io": legacy("t")}, ids.Snapshot())
	require.False(t, ids.Resolved())

	// second pass: the rest is known, tenancy is not asked again and keeps its value
	rest := func(_ context.Context, name string) (ResolvedIdentity, error) {
		if name == "tenancy.kcp.io" {
			return legacy("changed"), nil
		}
		return legacy(name), nil
	}
	require.NoError(t, ids.Resolve(ctx, rest))
	require.True(t, ids.Resolved())
	require.Equal(t, "t", ids.Snapshot()["tenancy.kcp.io"].Hash, "Resolve never replaces a resolved identity")

	got, _, err := decorateWildcardPathsWithResourceIdentities("/clusters/*/apis/migration.kcp.io/v1alpha1/apiexportstoragerekeys", ids)
	require.NoError(t, err)
	require.Equal(t, "/clusters/*/apis/migration.kcp.io/v1alpha1/apiexportstoragerekeys:migration.kcp.io", got)
	got, _, err = decorateWildcardPathsWithResourceIdentities("/clusters/*/apis/tenancy.kcp.io/v1alpha1/workspaces/root", ids)
	require.NoError(t, err)
	require.Equal(t, "/clusters/*/apis/tenancy.kcp.io/v1alpha1/workspaces:t/root", got)
}

func TestWithRecorded(t *testing.T) {
	t.Parallel()
	previous := newKey(t)
	mounted := newKey(t)
	hash, err := previous.IdentityHash("tenancy.kcp.io")
	require.NoError(t, err)

	recordedSet := set("shard-1", previous.ID(), nil)
	recordedSet.Status.Exports = []adminv1alpha1.CoreIdentity{{Name: "tenancy.kcp.io", IdentityHash: hash, Source: adminv1alpha1.CoreIdentitySourceKey, KeyID: previous.ID(), Derivation: corekey.Derivation}}
	local := NewLocalCoreIdentitySet(kcpfake.NewClientset(recordedSet), "shard-1")

	classify := WithRecorded(local, NewClassifier(mounted, nil))

	// the identity derived from the previous key keeps its recorded source after a key change
	id := classify("tenancy.kcp.io", legacy(hash))
	require.Equal(t, ResolvedIdentity{Hash: hash, Source: adminv1alpha1.CoreIdentitySourceKey, KeyID: previous.ID(), Derivation: corekey.Derivation}, id)

	// a different hash for the same export is not what was recorded
	require.Equal(t, adminv1alpha1.CoreIdentitySourceLegacy, classify("tenancy.kcp.io", legacy("other")).Source)

	// unknown exports fall through to the inner classifier
	derived, err := mounted.IdentityHash("topology.kcp.io")
	require.NoError(t, err)
	require.Equal(t, adminv1alpha1.CoreIdentitySourceKey, classify("topology.kcp.io", legacy(derived)).Source)

	// no local set at all
	require.Equal(t, adminv1alpha1.CoreIdentitySourceLegacy, WithRecorded(NewLocalCoreIdentitySet(kcpfake.NewClientset(), ""), nil)("tenancy.kcp.io", legacy(hash)).Source)
}
