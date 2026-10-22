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

package apiidentity

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/kcp-dev/logicalcluster/v3"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

func export(cluster, name, identity string, created time.Time, resources ...string) *apisv1alpha2.APIExport {
	ae := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Annotations:       map[string]string{logicalcluster.AnnotationKey: cluster},
			CreationTimestamp: metav1.NewTime(created),
		},
		Status: apisv1alpha2.APIExportStatus{IdentityHash: identity},
	}
	for _, r := range resources {
		ae.Spec.Resources = append(ae.Spec.Resources, apisv1alpha2.ResourceSchema{Name: r, Group: "example.io", Schema: "v1." + r + ".example.io",
			Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}}})
	}
	return ae
}

func TestOwner(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	older := export("ws-a", "a", "shared", t0, "as")
	newer := export("ws-b", "b", "shared", t0.Add(time.Hour), "bs")

	require.Nil(t, Owner(Input{}))
	require.Equal(t, "a", Owner(Input{Holders: []*apisv1alpha2.APIExport{newer, older}}).Name, "oldest holder owns by default")

	pinned := &adminv1alpha1.APIIdentity{Spec: adminv1alpha1.APIIdentitySpec{Owner: &adminv1alpha1.ExportIdentityReference{Cluster: "ws-b", Export: "b"}}}
	require.Equal(t, "b", Owner(Input{Holders: []*apisv1alpha2.APIExport{newer, older}, Existing: pinned}).Name, "spec.owner wins")

	stranger := &adminv1alpha1.APIIdentity{Spec: adminv1alpha1.APIIdentitySpec{Owner: &adminv1alpha1.ExportIdentityReference{Cluster: "ws-c", Export: "c"}}}
	require.Equal(t, "a", Owner(Input{Holders: []*apisv1alpha2.APIExport{newer, older}, Existing: stranger}).Name, "a spec.owner that is no holder is ignored")
}

func TestConversionBlocked(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := export("ws-a", "a", "shared", t0, "as")
	b := export("ws-b", "b", "shared", t0, "bs")
	bAmbiguous := export("ws-b", "b", "shared", t0, "as")

	reason, _ := ConversionBlocked(Input{Holders: []*apisv1alpha2.APIExport{a}})
	require.Empty(t, reason, "a single holder is not shared")

	reason, _ = ConversionBlocked(Input{Holders: []*apisv1alpha2.APIExport{a, b}})
	require.Empty(t, reason, "distinct resources convert")

	reason, msg := ConversionBlocked(Input{Holders: []*apisv1alpha2.APIExport{a, bAmbiguous}})
	require.Equal(t, adminv1alpha1.APIIdentityReasonAmbiguousResource, reason)
	require.Contains(t, msg, "as.example.io served by ws-a|a, ws-b|b")

	held := &adminv1alpha1.APIIdentity{Spec: adminv1alpha1.APIIdentitySpec{Convert: ptr.To(false)}}
	reason, _ = ConversionBlocked(Input{Holders: []*apisv1alpha2.APIExport{a, b}, Existing: held})
	require.Equal(t, adminv1alpha1.APIIdentityReasonConversionHeld, reason)
}

func TestDerive(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := t0.Add(48 * time.Hour)

	owner := export("ws-net", "networking", "ws-net-1", t0, "vnets", "subnets")
	owner.Status.IdentityAliasHashes = []string{"old-shared"}
	adopter := export("ws-compute", "compute", "ws-compute-1", t0, "vms")
	adopter.Status.Storage = &apisv1alpha2.StorageStatus{Resources: []apisv1alpha2.ResourceStorageStatus{
		{GroupResource: apisv1alpha2.GroupResource{Group: "example.io", Resource: "vms"}, Keys: []string{"ws-compute-1"}},
		{GroupResource: apisv1alpha2.GroupResource{Group: "example.io", Resource: "vnets"}, Keys: []string{"ws-net-1"}},
	}}
	claimer := export("ws-x", "x", "ws-x-1", t0, "xs")
	claimer.Spec.PermissionClaims = []apisv1alpha2.PermissionClaim{
		{GroupResource: apisv1alpha2.GroupResource{Group: "example.io", Resource: "vnets"}, IdentityHash: "old-shared"},
		{GroupResource: apisv1alpha2.GroupResource{Group: "example.io", Resource: "unrelated"}, IdentityHash: "old-shared"},
	}

	got := Derive(Input{
		Identity: "ws-net-1",
		Holders:  []*apisv1alpha2.APIExport{owner},
		Adopters: []*apisv1alpha2.APIExport{adopter},
		Claimers: []*apisv1alpha2.APIExport{claimer},
		IsCore:   func(*apisv1alpha2.APIExport) bool { return false },
		Now:      now,
	})

	require.Equal(t, "ws-net-1", got.Name)
	require.Equal(t, &adminv1alpha1.ExportIdentityReference{Cluster: "ws-net", Export: "networking"}, got.Status.Owner)
	require.Equal(t, []apisv1alpha2.GroupResource{{Group: "example.io", Resource: "subnets"}, {Group: "example.io", Resource: "vnets"}}, got.Status.GroupResources)
	require.False(t, got.Status.Core)
	require.Empty(t, got.Status.LegacyHolders)

	require.Len(t, got.Status.StorageSharedWith, 1)
	require.Equal(t, adminv1alpha1.ExportIdentityReference{Cluster: "ws-compute", Export: "compute"}, got.Status.StorageSharedWith[0].Export)
	require.Equal(t, []apisv1alpha2.GroupResource{{Group: "example.io", Resource: "vnets"}}, got.Status.StorageSharedWith[0].Resources, "only the resources on this prefix")
	require.True(t, conditions.IsTrue(got, adminv1alpha1.APIIdentityStorageShared))

	require.Len(t, got.Status.Aliases, 1)
	require.Equal(t, "old-shared", got.Status.Aliases[0].Identity)
	require.Equal(t, int32(1), got.Status.Aliases[0].References, "only claims for a resource the owner serves count")
	require.Equal(t, now, got.Status.Aliases[0].Since.Time)
	require.True(t, conditions.IsTrue(got, adminv1alpha1.APIIdentityAliased))
	require.False(t, conditions.Has(got, adminv1alpha1.APIIdentityOrphaned))

	// since is preserved across derivations
	again := Derive(Input{Identity: "ws-net-1", Holders: []*apisv1alpha2.APIExport{owner}, Existing: got, Now: now.Add(time.Hour)})
	require.Equal(t, now, again.Status.Aliases[0].Since.Time)
	require.Equal(t, int32(0), again.Status.Aliases[0].References)

	// retirement: not before the grace period, unless forced
	require.Empty(t, RetirableAliases(again, now.Add(time.Hour)))
	require.Equal(t, []string{"old-shared"}, RetirableAliases(again, now.Add(AliasRetirementGrace+time.Hour)))
	again.Spec.RetireAliases = true
	require.Equal(t, []string{"old-shared"}, RetirableAliases(again, now))
}

func TestDeriveLegacySharedAndOrphaned(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := export("ws-a", "a", "shared", t0, "as")
	b := export("ws-b", "b", "shared", t0.Add(time.Hour), "bs")

	got := Derive(Input{Identity: "shared", Holders: []*apisv1alpha2.APIExport{b, a}, Now: t0})
	require.Equal(t, "a", got.Status.Owner.Export)
	require.Equal(t, []adminv1alpha1.ExportIdentityReference{{Cluster: "ws-a", Export: "a"}, {Cluster: "ws-b", Export: "b"}}, got.Status.LegacyHolders)
	c := conditions.Get(got, adminv1alpha1.APIIdentityLegacyShared)
	require.NotNil(t, c)
	require.Equal(t, "True", string(c.Status))
	require.Equal(t, adminv1alpha1.APIIdentityReasonConverting, c.Reason)

	// after the conversion b holds a fresh identity; the shared one is a's.
	b.Status.IdentityHash = "ws-b-fresh"
	got = Derive(Input{Identity: "shared", Holders: []*apisv1alpha2.APIExport{a}, Existing: got, Now: t0})
	require.Empty(t, got.Status.LegacyHolders)
	require.False(t, conditions.Has(got, adminv1alpha1.APIIdentityLegacyShared))

	// orphaned: the owner is gone, what was known is kept.
	got = Derive(Input{Identity: "shared", Existing: got, Now: t0})
	require.Nil(t, got.Status.Owner)
	require.Equal(t, []apisv1alpha2.GroupResource{{Group: "example.io", Resource: "as"}}, got.Status.GroupResources)
	require.True(t, conditions.IsTrue(got, adminv1alpha1.APIIdentityOrphaned))
}
