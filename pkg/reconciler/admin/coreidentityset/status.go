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

// Package coreidentityset maintains the shard's CoreIdentitySet: the report
// of which identities the shard uses for the core APIExports, where they
// came from, whether the mounted core identities key is still the active
// one, and whether the shard agrees with the root shard and its peers.
package coreidentityset

import (
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"

	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"

	"github.com/kcp-dev/kcp/pkg/server/bootstrap"
)

// Input is everything needed to compute a shard's CoreIdentitySet status.
type Input struct {
	// ShardName is the name of this shard.
	ShardName string
	// ObservedKeyID is the ID of the mounted core identities key, or "".
	ObservedKeyID string
	// RecordedKeyID is the active key ID recorded so far, or "".
	RecordedKeyID string
	// ExportNames are the core APIExports the shard needs identities for.
	ExportNames []string
	// Identities are the identities resolved so far, by export name.
	Identities map[string]bootstrap.ResolvedIdentity
	// RootView maps export names to the identity hashes the root shard
	// uses, as far as they are visible (through the cache server). nil when
	// nothing about the root shard is visible.
	RootView map[string]string
	// Peers are the CoreIdentitySets of the other shards.
	Peers []adminv1alpha1.CoreIdentitySet
	// Conflict is a configuration conflict detected by the shard, e.g. a
	// --root-identities-file entry that disagrees with an existing
	// identity. Empty when there is none.
	Conflict string
}

// Apply sets the status of the CoreIdentitySet from the input. Conditions
// are merged, so their transition times survive unchanged states.
func Apply(set *adminv1alpha1.CoreIdentitySet, in Input) {
	set.Status.Shard = in.ShardName
	set.Status.ObservedKeyID = in.ObservedKeyID
	set.Status.KeyID = in.RecordedKeyID
	if set.Status.KeyID == "" {
		// the first key a shard sees becomes the active key.
		set.Status.KeyID = in.ObservedKeyID
	}

	set.Status.Exports = set.Status.Exports[:0]
	for name, id := range in.Identities {
		if id.Hash == "" {
			continue
		}
		source := id.Source
		if source == "" {
			source = adminv1alpha1.CoreIdentitySourceLegacy
		}
		set.Status.Exports = append(set.Status.Exports, adminv1alpha1.CoreIdentity{
			Name:         name,
			IdentityHash: id.Hash,
			Source:       source,
			KeyID:        id.KeyID,
			Derivation:   id.Derivation,
		})
	}
	sort.Slice(set.Status.Exports, func(i, j int) bool { return set.Status.Exports[i].Name < set.Status.Exports[j].Name })

	conditions.Set(set, keyChanged(set.Status.KeyID, in.ObservedKeyID))
	conditions.Set(set, consistent(set, in))
}

func keyChanged(active, observed string) *conditionsv1alpha1.Condition {
	switch {
	case active == "":
		return conditions.FalseCondition(adminv1alpha1.CoreIdentitySetKeyChanged, adminv1alpha1.CoreIdentitySetReasonNoKey, conditionsv1alpha1.ConditionSeverityNone, "no core identities key has been recorded")
	case observed == "":
		return &conditionsv1alpha1.Condition{
			Type:     adminv1alpha1.CoreIdentitySetKeyChanged,
			Status:   corev1.ConditionTrue,
			Severity: conditionsv1alpha1.ConditionSeverityWarning,
			Reason:   adminv1alpha1.CoreIdentitySetReasonKeyRemoved,
			Message:  KeyChangedMessage(active, observed),
		}
	case active == observed:
		return conditions.FalseCondition(adminv1alpha1.CoreIdentitySetKeyChanged, "", conditionsv1alpha1.ConditionSeverityNone, "")
	default:
		return &conditionsv1alpha1.Condition{
			Type:     adminv1alpha1.CoreIdentitySetKeyChanged,
			Status:   corev1.ConditionTrue,
			Severity: conditionsv1alpha1.ConditionSeverityWarning,
			Reason:   adminv1alpha1.CoreIdentitySetReasonKeyChanged,
			Message:  KeyChangedMessage(active, observed),
		}
	}
}

// KeyChangedMessage explains a KeyChanged=True condition.
func KeyChangedMessage(active, observed string) string {
	if observed == "" {
		return fmt.Sprintf("the active key %s is not mounted anymore; existing identities keep working, new core APIs get no identity until it is restored", active)
	}
	return fmt.Sprintf("the mounted key %s differs from the active key %s; existing identities keep working, new core APIs get no identity until the previous key is restored", observed, active)
}

func consistent(set *adminv1alpha1.CoreIdentitySet, in Input) *conditionsv1alpha1.Condition {
	if in.Conflict != "" {
		return conditions.FalseCondition(adminv1alpha1.CoreIdentitySetConsistent, adminv1alpha1.CoreIdentitySetReasonMismatch, conditionsv1alpha1.ConditionSeverityError, "%s", in.Conflict)
	}

	var missing []string
	for _, name := range in.ExportNames {
		if id, ok := in.Identities[name]; !ok || id.Hash == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return conditions.UnknownCondition(adminv1alpha1.CoreIdentitySetConsistent, adminv1alpha1.CoreIdentitySetReasonUnresolved, "no identity yet for %s", strings.Join(missing, ", "))
	}

	if in.ShardName == corev1alpha1.RootShard {
		// the root workspace is the reference every other shard compares with.
		return conditions.TrueCondition(adminv1alpha1.CoreIdentitySetConsistent)
	}

	mine := map[string]string{}
	for _, export := range set.Status.Exports {
		mine[export.Name] = export.IdentityHash
	}

	if in.RootView != nil {
		var mismatches []string
		for _, name := range in.ExportNames {
			if rootHash, ok := in.RootView[name]; ok && rootHash != mine[name] {
				mismatches = append(mismatches, fmt.Sprintf("%s: this shard uses %s, the root shard uses %s", name, mine[name], rootHash))
			}
		}
		if len(mismatches) > 0 {
			return conditions.FalseCondition(adminv1alpha1.CoreIdentitySetConsistent, adminv1alpha1.CoreIdentitySetReasonMismatch, conditionsv1alpha1.ConditionSeverityError, "%s", strings.Join(mismatches, "; "))
		}
		return conditions.TrueCondition(adminv1alpha1.CoreIdentitySetConsistent)
	}

	compared := false
	var mismatches []string
	for i := range in.Peers {
		peer := &in.Peers[i]
		if peer.Status.Shard == in.ShardName {
			continue
		}
		for _, export := range peer.Status.Exports {
			hash, ok := mine[export.Name]
			if !ok || export.IdentityHash == "" {
				continue
			}
			compared = true
			if hash != export.IdentityHash {
				mismatches = append(mismatches, fmt.Sprintf("%s: this shard uses %s, shard %s uses %s", export.Name, hash, peer.Status.Shard, export.IdentityHash))
			}
		}
	}
	if len(mismatches) > 0 {
		return conditions.FalseCondition(adminv1alpha1.CoreIdentitySetConsistent, adminv1alpha1.CoreIdentitySetReasonMismatch, conditionsv1alpha1.ConditionSeverityError, "%s", strings.Join(mismatches, "; "))
	}
	if !compared {
		return conditions.UnknownCondition(adminv1alpha1.CoreIdentitySetConsistent, adminv1alpha1.CoreIdentitySetReasonNoPeers, "no other shard's identities are visible to compare with")
	}
	return conditions.TrueCondition(adminv1alpha1.CoreIdentitySetConsistent)
}

// ReadinessError says why a shard with this CoreIdentitySet is not ready to
// serve, or nil when it is:
//
//   - identities are not resolved yet,
//   - the configured identities conflict (Consistent=False on the root shard),
//   - a shard disagrees with the root shard or its peers (Consistent=False),
//   - a shard derived identities from the key and no other shard has
//     confirmed them yet (Consistent=Unknown with a Key-sourced identity).
//
// Existing shards with legacy identities stay ready while nobody is there
// to compare with: they cannot have diverged.
func ReadinessError(set *adminv1alpha1.CoreIdentitySet) error {
	cond := conditions.Get(set, adminv1alpha1.CoreIdentitySetConsistent)
	if cond == nil {
		return fmt.Errorf("core identities are not resolved yet")
	}
	switch cond.Status {
	case corev1.ConditionTrue:
		return nil
	case corev1.ConditionFalse:
		return fmt.Errorf("core identities are inconsistent: %s", cond.Message)
	}
	if cond.Reason == adminv1alpha1.CoreIdentitySetReasonUnresolved {
		return fmt.Errorf("core identities are not resolved yet: %s", cond.Message)
	}
	for _, export := range set.Status.Exports {
		if export.Source == adminv1alpha1.CoreIdentitySourceKey {
			return fmt.Errorf("the identity of %s was derived from the core identities key and no other shard has confirmed it yet: %s", export.Name, cond.Message)
		}
	}
	return nil
}
