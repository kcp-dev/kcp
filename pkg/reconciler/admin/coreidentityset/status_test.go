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

package coreidentityset

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"

	"github.com/kcp-dev/kcp/pkg/server/bootstrap"
)

var exportNames = []string{"tenancy.kcp.io", "topology.kcp.io"}

func resolved(hashes map[string]string, source adminv1alpha1.CoreIdentitySource) map[string]bootstrap.ResolvedIdentity {
	ids := map[string]bootstrap.ResolvedIdentity{}
	for name, hash := range hashes {
		id := bootstrap.ResolvedIdentity{Hash: hash, Source: source}
		if source == adminv1alpha1.CoreIdentitySourceKey {
			id.KeyID, id.Derivation = "key-1", "hkdf-sha256/v1"
		}
		ids[name] = id
	}
	return ids
}

func peer(shard string, hashes map[string]string) adminv1alpha1.CoreIdentitySet {
	set := adminv1alpha1.CoreIdentitySet{ObjectMeta: metav1.ObjectMeta{Name: shard}}
	set.Status.Shard = shard
	for name, hash := range hashes {
		set.Status.Exports = append(set.Status.Exports, adminv1alpha1.CoreIdentity{Name: name, IdentityHash: hash, Source: adminv1alpha1.CoreIdentitySourceLegacy})
	}
	return set
}

func condition(t *testing.T, set *adminv1alpha1.CoreIdentitySet, typ string) (corev1.ConditionStatus, string) {
	t.Helper()
	c := conditions.Get(set, adminv1alpha1.CoreIdentitySetConsistent)
	if typ == "KeyChanged" {
		c = conditions.Get(set, adminv1alpha1.CoreIdentitySetKeyChanged)
	}
	require.NotNil(t, c, "condition %s", typ)
	return c.Status, c.Reason
}

func TestApplyExportsAndKey(t *testing.T) {
	t.Parallel()

	set := &adminv1alpha1.CoreIdentitySet{}
	Apply(set, Input{
		ShardName:     "shard-1",
		ObservedKeyID: "key-1",
		ExportNames:   exportNames,
		Identities:    resolved(map[string]string{"topology.kcp.io": "b", "tenancy.kcp.io": "a"}, adminv1alpha1.CoreIdentitySourceKey),
		RootView:      map[string]string{"tenancy.kcp.io": "a", "topology.kcp.io": "b"},
	})

	require.Equal(t, "shard-1", set.Status.Shard)
	require.Equal(t, "key-1", set.Status.KeyID, "the first observed key becomes the active key")
	require.Equal(t, "key-1", set.Status.ObservedKeyID)
	require.Equal(t, []adminv1alpha1.CoreIdentity{
		{Name: "tenancy.kcp.io", IdentityHash: "a", Source: adminv1alpha1.CoreIdentitySourceKey, KeyID: "key-1", Derivation: "hkdf-sha256/v1"},
		{Name: "topology.kcp.io", IdentityHash: "b", Source: adminv1alpha1.CoreIdentitySourceKey, KeyID: "key-1", Derivation: "hkdf-sha256/v1"},
	}, set.Status.Exports, "exports are sorted by name")

	status, _ := condition(t, set, "KeyChanged")
	require.Equal(t, corev1.ConditionFalse, status)
	status, _ = condition(t, set, "Consistent")
	require.Equal(t, corev1.ConditionTrue, status)
	require.NoError(t, ReadinessError(set))
}

func TestKeyChanged(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		recorded, observed string
		wantStatus         corev1.ConditionStatus
		wantReason         string
		wantKeyID          string
	}{
		"no key ever":   {wantStatus: corev1.ConditionFalse, wantReason: adminv1alpha1.CoreIdentitySetReasonNoKey},
		"first key":     {observed: "k1", wantStatus: corev1.ConditionFalse, wantKeyID: "k1"},
		"same key":      {recorded: "k1", observed: "k1", wantStatus: corev1.ConditionFalse, wantKeyID: "k1"},
		"different key": {recorded: "k1", observed: "k2", wantStatus: corev1.ConditionTrue, wantReason: adminv1alpha1.CoreIdentitySetReasonKeyChanged, wantKeyID: "k1"},
		"key removed":   {recorded: "k1", wantStatus: corev1.ConditionTrue, wantReason: adminv1alpha1.CoreIdentitySetReasonKeyRemoved, wantKeyID: "k1"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			set := &adminv1alpha1.CoreIdentitySet{}
			Apply(set, Input{ShardName: "root", RecordedKeyID: tc.recorded, ObservedKeyID: tc.observed, ExportNames: exportNames,
				Identities: resolved(map[string]string{"tenancy.kcp.io": "a", "topology.kcp.io": "b"}, adminv1alpha1.CoreIdentitySourceLegacy)})
			status, reason := condition(t, set, "KeyChanged")
			require.Equal(t, tc.wantStatus, status)
			require.Equal(t, tc.wantReason, reason)
			require.Equal(t, tc.wantKeyID, set.Status.KeyID, "the active key never changes once recorded")
		})
	}
}

func TestConsistentAndReadiness(t *testing.T) {
	t.Parallel()

	legacy := resolved(map[string]string{"tenancy.kcp.io": "a", "topology.kcp.io": "b"}, adminv1alpha1.CoreIdentitySourceLegacy)
	fromKey := resolved(map[string]string{"tenancy.kcp.io": "a", "topology.kcp.io": "b"}, adminv1alpha1.CoreIdentitySourceKey)

	tests := map[string]struct {
		in         Input
		wantStatus corev1.ConditionStatus
		wantReason string
		wantReady  bool
	}{
		"unresolved": {
			in:         Input{ShardName: "shard-1", ExportNames: exportNames, Identities: resolved(map[string]string{"tenancy.kcp.io": "a"}, adminv1alpha1.CoreIdentitySourceLegacy)},
			wantStatus: corev1.ConditionUnknown, wantReason: adminv1alpha1.CoreIdentitySetReasonUnresolved,
		},
		"root is the reference": {
			in:         Input{ShardName: "root", ExportNames: exportNames, Identities: legacy, Peers: []adminv1alpha1.CoreIdentitySet{peer("shard-1", map[string]string{"tenancy.kcp.io": "x"})}},
			wantStatus: corev1.ConditionTrue, wantReady: true,
		},
		"root with a conflicting dev file": {
			in:         Input{ShardName: "root", ExportNames: exportNames, Identities: legacy, Conflict: "entry for tenancy.kcp.io disagrees"},
			wantStatus: corev1.ConditionFalse, wantReason: adminv1alpha1.CoreIdentitySetReasonMismatch,
		},
		"matches root view": {
			in:         Input{ShardName: "shard-1", ExportNames: exportNames, Identities: fromKey, RootView: map[string]string{"tenancy.kcp.io": "a", "topology.kcp.io": "b"}},
			wantStatus: corev1.ConditionTrue, wantReady: true,
		},
		"root view wins over a disagreeing peer": {
			in: Input{ShardName: "shard-1", ExportNames: exportNames, Identities: legacy,
				RootView: map[string]string{"tenancy.kcp.io": "a", "topology.kcp.io": "b"},
				Peers:    []adminv1alpha1.CoreIdentitySet{peer("shard-2", map[string]string{"tenancy.kcp.io": "x"})}},
			wantStatus: corev1.ConditionTrue, wantReady: true,
		},
		"differs from root view": {
			in:         Input{ShardName: "shard-1", ExportNames: exportNames, Identities: legacy, RootView: map[string]string{"tenancy.kcp.io": "x"}},
			wantStatus: corev1.ConditionFalse, wantReason: adminv1alpha1.CoreIdentitySetReasonMismatch,
		},
		"matches peers without root view": {
			in:         Input{ShardName: "shard-1", ExportNames: exportNames, Identities: fromKey, Peers: []adminv1alpha1.CoreIdentitySet{peer("shard-2", map[string]string{"tenancy.kcp.io": "a"})}},
			wantStatus: corev1.ConditionTrue, wantReady: true,
		},
		"differs from a peer without root view": {
			in:         Input{ShardName: "shard-1", ExportNames: exportNames, Identities: legacy, Peers: []adminv1alpha1.CoreIdentitySet{peer("shard-2", map[string]string{"topology.kcp.io": "x"})}},
			wantStatus: corev1.ConditionFalse, wantReason: adminv1alpha1.CoreIdentitySetReasonMismatch,
		},
		"legacy identities with nobody to compare with stay ready": {
			in:         Input{ShardName: "shard-1", ExportNames: exportNames, Identities: legacy},
			wantStatus: corev1.ConditionUnknown, wantReason: adminv1alpha1.CoreIdentitySetReasonNoPeers, wantReady: true,
		},
		"key-derived identities need confirmation": {
			in:         Input{ShardName: "shard-1", ExportNames: exportNames, Identities: fromKey},
			wantStatus: corev1.ConditionUnknown, wantReason: adminv1alpha1.CoreIdentitySetReasonNoPeers,
		},
		"own set among peers is ignored": {
			in:         Input{ShardName: "shard-1", ExportNames: exportNames, Identities: fromKey, Peers: []adminv1alpha1.CoreIdentitySet{peer("shard-1", map[string]string{"tenancy.kcp.io": "x"})}},
			wantStatus: corev1.ConditionUnknown, wantReason: adminv1alpha1.CoreIdentitySetReasonNoPeers,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			set := &adminv1alpha1.CoreIdentitySet{}
			Apply(set, tc.in)
			status, reason := condition(t, set, "Consistent")
			require.Equal(t, tc.wantStatus, status)
			require.Equal(t, tc.wantReason, reason)
			if tc.wantReady {
				require.NoError(t, ReadinessError(set))
			} else {
				require.Error(t, ReadinessError(set))
			}
		})
	}
}

func TestApplyKeepsTransitionTimes(t *testing.T) {
	t.Parallel()

	in := Input{ShardName: "root", ObservedKeyID: "k1", ExportNames: exportNames,
		Identities: resolved(map[string]string{"tenancy.kcp.io": "a", "topology.kcp.io": "b"}, adminv1alpha1.CoreIdentitySourceLegacy)}
	set := &adminv1alpha1.CoreIdentitySet{}
	Apply(set, in)
	first := conditions.Get(set, adminv1alpha1.CoreIdentitySetConsistent).LastTransitionTime
	time.Sleep(1100 * time.Millisecond)
	Apply(set, in)
	require.Equal(t, first, conditions.Get(set, adminv1alpha1.CoreIdentitySetConsistent).LastTransitionTime)
}

func TestRootView(t *testing.T) {
	t.Parallel()

	rootSet := peer("root", map[string]string{"tenancy.kcp.io": "set"})
	view := RootView([]*adminv1alpha1.CoreIdentitySet{&rootSet}, func() ([]*apisv1alpha2.APIExport, error) { t.Fatal("not expected"); return nil, nil })
	require.Equal(t, map[string]string{"tenancy.kcp.io": "set"}, view)

	other := peer("shard-1", map[string]string{"tenancy.kcp.io": "x"})
	view = RootView([]*adminv1alpha1.CoreIdentitySet{&other}, func() ([]*apisv1alpha2.APIExport, error) {
		return []*apisv1alpha2.APIExport{{ObjectMeta: metav1.ObjectMeta{Name: "tenancy.kcp.io"}, Status: apisv1alpha2.APIExportStatus{IdentityHash: "export"}}}, nil
	})
	require.Equal(t, map[string]string{"tenancy.kcp.io": "export"}, view)

	view = RootView(nil, func() ([]*apisv1alpha2.APIExport, error) { return nil, nil })
	require.Nil(t, view)
}
