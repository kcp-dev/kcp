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
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kcp-dev/logicalcluster/v3"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"

	"github.com/kcp-dev/kcp/pkg/indexers"
)

// AliasRetirementGrace is how long an alias of a converted identity is kept
// after the last permission claim referencing it went away. spec.retireAliases
// retires aliases at once.
const AliasRetirementGrace = 7 * 24 * time.Hour

// Input is everything Derive needs about one identity, as seen through the
// installation-wide (cache server) view of APIExports.
type Input struct {
	// Identity is the identity the APIIdentity object is about.
	Identity string
	// Holders are the exports whose status.identityHash is Identity.
	Holders []*apisv1alpha2.APIExport
	// Adopters are the exports whose status.storage reports Identity as a
	// storage prefix of one of their resources while their own identity is
	// another: adopted storage.
	Adopters []*apisv1alpha2.APIExport
	// Claimers are the exports with a resolved permission claim naming
	// Identity or one of the owner's aliases.
	Claimers []*apisv1alpha2.APIExport
	// Existing is the current APIIdentity object, nil if none.
	Existing *adminv1alpha1.APIIdentity
	// IsCore reports whether an export is one of kcp's core exports.
	IsCore func(*apisv1alpha2.APIExport) bool
	// Now is the time of the reconcile.
	Now time.Time
}

// Owner returns the holder that keeps the identity: the one spec.owner
// selects if it is a holder, else the oldest holder. nil without holders.
func Owner(in Input) *apisv1alpha2.APIExport {
	if len(in.Holders) == 0 {
		return nil
	}
	if in.Existing != nil && in.Existing.Spec.Owner != nil {
		for _, h := range in.Holders {
			if ref(h) == *in.Existing.Spec.Owner {
				return h
			}
		}
	}
	holders := append([]*apisv1alpha2.APIExport(nil), in.Holders...)
	sort.SliceStable(holders, func(i, j int) bool {
		ti, tj := holders[i].CreationTimestamp.Time, holders[j].CreationTimestamp.Time
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return key(holders[i]) < key(holders[j])
	})
	return holders[0]
}

// ConversionBlocked reports why the conversion of a legacy shared identity
// cannot run: ConversionHeld when spec.convert is false, AmbiguousResource
// when two holders serve the same resource (claims naming the shared identity
// for it could not be attributed to one holder). Empty when it may run.
func ConversionBlocked(in Input) (reason, message string) {
	if len(in.Holders) < 2 {
		return "", ""
	}
	if in.Existing != nil && in.Existing.Spec.Convert != nil && !*in.Existing.Spec.Convert {
		return adminv1alpha1.APIIdentityReasonConversionHeld, "spec.convert is false; every holder keeps the identity"
	}
	served := map[apisv1alpha2.GroupResource][]string{}
	for _, h := range in.Holders {
		for _, rs := range h.Spec.Resources {
			gr := apisv1alpha2.GroupResource{Group: rs.Group, Resource: rs.Name}
			served[gr] = append(served[gr], key(h))
		}
	}
	var ambiguous []string
	for gr, holders := range served {
		if len(holders) > 1 {
			sort.Strings(holders)
			ambiguous = append(ambiguous, gr.Resource+"."+gr.Group+" served by "+strings.Join(holders, ", "))
		}
	}
	if len(ambiguous) > 0 {
		sort.Strings(ambiguous)
		return adminv1alpha1.APIIdentityReasonAmbiguousResource, "several holders serve the same resource: " + strings.Join(ambiguous, "; ")
	}
	return "", ""
}

// Derive computes the APIIdentity status for the input. The returned object
// carries the existing spec and metadata; only status is derived.
func Derive(in Input) *adminv1alpha1.APIIdentity {
	var out *adminv1alpha1.APIIdentity
	if in.Existing != nil {
		out = in.Existing.DeepCopy()
	} else {
		out = &adminv1alpha1.APIIdentity{ObjectMeta: metav1.ObjectMeta{Name: in.Identity}}
	}
	previous := out.Status
	out.Status = adminv1alpha1.APIIdentityStatus{Conditions: previous.Conditions}

	owner := Owner(in)
	if owner == nil {
		// orphaned: the holder went away, bindings may still store under the
		// identity. Keep what was known about it.
		out.Status.GroupResources = previous.GroupResources
		out.Status.Core = previous.Core
		conditions.MarkTrue(out, adminv1alpha1.APIIdentityOrphaned)
		conditions.Delete(out, adminv1alpha1.APIIdentityLegacyShared)
		conditions.Delete(out, adminv1alpha1.APIIdentityAliased)
		shares(out, in)
		return out
	}
	conditions.Delete(out, adminv1alpha1.APIIdentityOrphaned)

	ownerRef := ref(owner)
	out.Status.Owner = &ownerRef
	out.Status.Core = in.IsCore != nil && in.IsCore(owner)

	grs := sets.New[apisv1alpha2.GroupResource]()
	for _, rs := range owner.Spec.Resources {
		grs.Insert(apisv1alpha2.GroupResource{Group: rs.Group, Resource: rs.Name})
	}
	out.Status.GroupResources = sortedGRs(grs)

	if len(in.Holders) > 1 {
		for _, h := range in.Holders {
			out.Status.LegacyHolders = append(out.Status.LegacyHolders, ref(h))
		}
		sort.Slice(out.Status.LegacyHolders, func(i, j int) bool {
			a, b := out.Status.LegacyHolders[i], out.Status.LegacyHolders[j]
			return a.Cluster+"|"+a.Export < b.Cluster+"|"+b.Export
		})
		reason, message := ConversionBlocked(in)
		if reason == "" {
			reason, message = adminv1alpha1.APIIdentityReasonConverting, "every holder but the owner is being moved onto a fresh identity"
		}
		conditions.Set(out, &conditionsv1alpha1.Condition{
			Type:     adminv1alpha1.APIIdentityLegacyShared,
			Status:   corev1.ConditionTrue,
			Severity: conditionsv1alpha1.ConditionSeverityWarning,
			Reason:   reason,
			Message:  message,
		})
	} else {
		conditions.Delete(out, adminv1alpha1.APIIdentityLegacyShared)
	}

	// aliases: former identities of the owner, with the claims still naming
	// them for one of the owner's resources counted.
	previousAliases := map[string]adminv1alpha1.IdentityAlias{}
	for _, a := range previous.Aliases {
		previousAliases[a.Identity] = a
	}
	for _, alias := range owner.Status.IdentityAliasHashes {
		entry := adminv1alpha1.IdentityAlias{Identity: alias, GroupResources: out.Status.GroupResources}
		if prev, ok := previousAliases[alias]; ok && prev.Since != nil {
			entry.Since = prev.Since
		} else {
			now := metav1.NewTime(in.Now)
			entry.Since = &now
		}
		for _, claimer := range in.Claimers {
			for _, claim := range claimer.ResolvedPermissionClaims() {
				if claim.IdentityHash == alias && indexers.ExportServesGR(owner, claim.Group, claim.Resource) {
					entry.References++
				}
			}
		}
		out.Status.Aliases = append(out.Status.Aliases, entry)
	}
	if len(out.Status.Aliases) > 0 {
		conditions.MarkTrue(out, adminv1alpha1.APIIdentityAliased)
	} else {
		conditions.Delete(out, adminv1alpha1.APIIdentityAliased)
	}

	shares(out, in)
	return out
}

// RetirableAliases returns the aliases of the derived object that may be
// retired now: all of them when spec.retireAliases is set, otherwise those
// without references for longer than the grace period.
func RetirableAliases(obj *adminv1alpha1.APIIdentity, now time.Time) []string {
	var retire []string
	for _, a := range obj.Status.Aliases {
		switch {
		case obj.Spec.RetireAliases:
			retire = append(retire, a.Identity)
		case a.References == 0 && a.Since != nil && now.Sub(a.Since.Time) >= AliasRetirementGrace:
			retire = append(retire, a.Identity)
		}
	}
	return retire
}

func shares(out *adminv1alpha1.APIIdentity, in Input) {
	for _, adopter := range in.Adopters {
		share := adminv1alpha1.StorageShare{Export: ref(adopter)}
		if adopter.Status.Storage != nil {
			for _, rs := range adopter.Status.Storage.Resources {
				for _, k := range rs.Keys {
					if k == in.Identity {
						share.Resources = append(share.Resources, rs.GroupResource)
						break
					}
				}
			}
		}
		if len(share.Resources) == 0 {
			continue
		}
		sort.Slice(share.Resources, func(i, j int) bool { return grLess(share.Resources[i], share.Resources[j]) })
		out.Status.StorageSharedWith = append(out.Status.StorageSharedWith, share)
	}
	sort.Slice(out.Status.StorageSharedWith, func(i, j int) bool {
		a, b := out.Status.StorageSharedWith[i].Export, out.Status.StorageSharedWith[j].Export
		return a.Cluster+"|"+a.Export < b.Cluster+"|"+b.Export
	})
	if len(out.Status.StorageSharedWith) > 0 {
		conditions.MarkTrue(out, adminv1alpha1.APIIdentityStorageShared)
	} else {
		conditions.Delete(out, adminv1alpha1.APIIdentityStorageShared)
	}
}

func ref(export *apisv1alpha2.APIExport) adminv1alpha1.ExportIdentityReference {
	return adminv1alpha1.ExportIdentityReference{Cluster: logicalcluster.From(export).String(), Export: export.Name}
}

func key(export *apisv1alpha2.APIExport) string {
	return logicalcluster.From(export).String() + "|" + export.Name
}

func grLess(a, b apisv1alpha2.GroupResource) bool {
	if a.Group != b.Group {
		return a.Group < b.Group
	}
	return a.Resource < b.Resource
}

func sortedGRs(grs sets.Set[apisv1alpha2.GroupResource]) []apisv1alpha2.GroupResource {
	list := grs.UnsortedList()
	sort.Slice(list, func(i, j int) bool { return grLess(list[i], list[j]) })
	return list
}

// isRootExport reports whether the export lives in the root workspace.
func isRootExport(export *apisv1alpha2.APIExport) bool {
	return logicalcluster.From(export) == core.RootCluster
}
