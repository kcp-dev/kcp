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
	"errors"
	"fmt"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/rest"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"

	configshard "github.com/kcp-dev/kcp/config/shard"
	"github.com/kcp-dev/kcp/pkg/identity/corekey"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/identitycache"
)

// Provider returns the identity of the named core APIExport. It returns an
// error wrapping ErrUnknown when it has no answer, so that a chain of
// providers can move on to the next one.
type Provider func(ctx context.Context, exportName string) (ResolvedIdentity, error)

// ErrUnknown is returned (wrapped) by a Provider that has no identity for
// the export.
var ErrUnknown = errors.New("identity unknown")

// Chain tries the providers in order and returns the first identity found.
// Every provider is tried; the errors of all of them are reported when none
// has an answer.
func Chain(providers ...Provider) Provider {
	return func(ctx context.Context, exportName string) (ResolvedIdentity, error) {
		var errs []error
		for _, p := range providers {
			if p == nil {
				continue
			}
			id, err := p(ctx, exportName)
			if err == nil && id.Hash != "" {
				return id, nil
			}
			if err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) == 0 {
			return ResolvedIdentity{}, ErrUnknown
		}
		return ResolvedIdentity{}, utilerrors.NewAggregate(errs)
	}
}

// RootAPIExportProvider reads the identity from the APIExport in the root
// workspace through the given client config: the loopback config on the
// root shard, or a root shard kubeconfig on other shards (legacy).
func RootAPIExportProvider(config *rest.Config) Provider {
	return func(ctx context.Context, exportName string) (ResolvedIdentity, error) {
		client, err := kcpclientset.NewForConfig(config)
		if err != nil {
			return ResolvedIdentity{}, err
		}
		apiExport, err := client.Cluster(core.RootCluster.Path()).ApisV1alpha2().APIExports().Get(ctx, exportName, metav1.GetOptions{})
		if err != nil {
			return ResolvedIdentity{}, fmt.Errorf("root workspace: %w", err)
		}
		if apiExport.Status.IdentityHash == "" {
			return ResolvedIdentity{}, fmt.Errorf("root workspace: APIExport %s has no identity yet: %w", exportName, ErrUnknown)
		}
		return legacy(apiExport.Status.IdentityHash), nil
	}
}

// LegacyConfigMapProvider reads the identity from the deprecated
// apiexport-identity-cache ConfigMap in the shard-local system:shard logical
// cluster.
func LegacyConfigMapProvider(kubeClusterClient kcpkubernetesclientset.ClusterInterface) Provider {
	return func(ctx context.Context, exportName string) (ResolvedIdentity, error) {
		cm, err := kubeClusterClient.Cluster(configshard.SystemShardCluster.Path()).CoreV1().ConfigMaps("default").Get(ctx, identitycache.ConfigMapName, metav1.GetOptions{})
		if err != nil {
			return ResolvedIdentity{}, fmt.Errorf("local identity cache: %w", err)
		}
		hash, found := cm.Data[exportName]
		if !found || hash == "" {
			return ResolvedIdentity{}, fmt.Errorf("local identity cache has no entry for %s: %w", exportName, ErrUnknown)
		}
		return legacy(hash), nil
	}
}

// LocalCoreIdentitySet reads the shard's own CoreIdentitySet in the
// shard-local system:shard logical cluster. shardName may be empty, in which
// case the single object in the logical cluster is used (virtual-workspaces
// servers do not know their shard's name).
type LocalCoreIdentitySet struct {
	client    kcpclientset.ClusterInterface
	shardName string
}

// NewLocalCoreIdentitySet creates a reader for the local CoreIdentitySet.
func NewLocalCoreIdentitySet(client kcpclientset.ClusterInterface, shardName string) *LocalCoreIdentitySet {
	return &LocalCoreIdentitySet{client: client, shardName: shardName}
}

// Get returns the local CoreIdentitySet, or a NotFound error.
func (l *LocalCoreIdentitySet) Get(ctx context.Context) (*adminv1alpha1.CoreIdentitySet, error) {
	sets := l.client.Cluster(configshard.SystemShardCluster.Path()).AdminV1alpha1().CoreIdentitySets()
	if l.shardName != "" {
		return sets.Get(ctx, l.shardName, metav1.GetOptions{})
	}
	list, err := sets.List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return nil, apierrors.NewNotFound(adminv1alpha1.Resource("coreidentitysets"), "")
	}
	return &list.Items[0], nil
}

// RecordedKeyID returns the active key ID recorded in the local
// CoreIdentitySet, or "" when there is none (yet).
func (l *LocalCoreIdentitySet) RecordedKeyID(ctx context.Context) (string, error) {
	set, err := l.Get(ctx)
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return set.Status.KeyID, nil
}

// Provider returns a Provider that answers from the local CoreIdentitySet.
// The recorded source is kept, so a restart reports the same sources as the
// run that resolved the identities.
func (l *LocalCoreIdentitySet) Provider() Provider {
	return func(ctx context.Context, exportName string) (ResolvedIdentity, error) {
		set, err := l.Get(ctx)
		if err != nil {
			return ResolvedIdentity{}, fmt.Errorf("local CoreIdentitySet: %w", err)
		}
		for _, export := range set.Status.Exports {
			if export.Name == exportName && export.IdentityHash != "" {
				return ResolvedIdentity{
					Hash:       export.IdentityHash,
					Source:     export.Source,
					KeyID:      export.KeyID,
					Derivation: export.Derivation,
				}, nil
			}
		}
		return ResolvedIdentity{}, fmt.Errorf("local CoreIdentitySet has no entry for %s: %w", exportName, ErrUnknown)
	}
}

// CacheProvider reads identities from the cache server: the root workspace's
// APIExports replicated there, and the CoreIdentitySets of the other shards.
// The root shard's view wins; otherwise all shards reporting the export must
// agree.
func CacheProvider(cacheKcpClusterClient kcpclientset.ClusterInterface) Provider {
	return func(ctx context.Context, exportName string) (ResolvedIdentity, error) {
		var errs []error

		exports, err := cacheKcpClusterClient.Cluster(core.RootCluster.Path()).ApisV1alpha2().APIExports().List(ctx, metav1.ListOptions{})
		if err != nil {
			errs = append(errs, fmt.Errorf("cache server: root APIExports: %w", err))
		} else {
			for i := range exports.Items {
				if exports.Items[i].Name == exportName && exports.Items[i].Status.IdentityHash != "" {
					return legacy(exports.Items[i].Status.IdentityHash), nil
				}
			}
		}

		sets, err := cacheKcpClusterClient.AdminV1alpha1().CoreIdentitySets().List(ctx, metav1.ListOptions{})
		if err != nil {
			errs = append(errs, fmt.Errorf("cache server: CoreIdentitySets: %w", err))
		} else if id, found, err := IdentityFromSets(sets.Items, exportName); err != nil {
			errs = append(errs, fmt.Errorf("cache server: %w", err))
		} else if found {
			return id, nil
		}

		if len(errs) > 0 {
			return ResolvedIdentity{}, utilerrors.NewAggregate(errs)
		}
		return ResolvedIdentity{}, fmt.Errorf("cache server has no identity for %s: %w", exportName, ErrUnknown)
	}
}

// IdentityFromSets picks the identity of the export from a number of
// CoreIdentitySets. The root shard's set wins. Otherwise every set reporting
// the export must agree; a disagreement is an error, because guessing would
// be worse than waiting.
func IdentityFromSets(sets []adminv1alpha1.CoreIdentitySet, exportName string) (ResolvedIdentity, bool, error) {
	var found *ResolvedIdentity
	var foundOn string
	for i := range sets {
		set := &sets[i]
		for _, export := range set.Status.Exports {
			if export.Name != exportName || export.IdentityHash == "" {
				continue
			}
			id := ResolvedIdentity{Hash: export.IdentityHash, Source: export.Source, KeyID: export.KeyID, Derivation: export.Derivation}
			if set.Status.Shard == corev1alpha1.RootShard {
				return id, true, nil
			}
			if found != nil && found.Hash != id.Hash {
				return ResolvedIdentity{}, false, fmt.Errorf("shards %s and %s disagree on the identity of %s (%s vs %s)", foundOn, set.Status.Shard, exportName, found.Hash, id.Hash)
			}
			if found == nil {
				found, foundOn = &id, set.Status.Shard
			}
		}
	}
	if found == nil {
		return ResolvedIdentity{}, false, nil
	}
	return *found, true, nil
}

// AdminWorkspaceProvider reads identities from the CoreIdentitySets served
// by the Admin workspace (/services/admin), which lists every shard's set.
// The client must point at the Admin workspace, e.g. the front-proxy's peers
// config.
func AdminWorkspaceProvider(adminClusterClient kcpclientset.ClusterInterface) Provider {
	return func(ctx context.Context, exportName string) (ResolvedIdentity, error) {
		sets, err := adminClusterClient.Cluster(core.RootCluster.Path()).AdminV1alpha1().CoreIdentitySets().List(ctx, metav1.ListOptions{})
		if err != nil {
			return ResolvedIdentity{}, fmt.Errorf("admin workspace: %w", err)
		}
		id, found, err := IdentityFromSets(sets.Items, exportName)
		if err != nil {
			return ResolvedIdentity{}, fmt.Errorf("admin workspace: %w", err)
		}
		if !found {
			return ResolvedIdentity{}, fmt.Errorf("admin workspace: no shard reports an identity for %s: %w", exportName, ErrUnknown)
		}
		return id, nil
	}
}

// KeyProvider derives the identity from the core identities key. The gate
// is consulted first and can refuse, e.g. when the mounted key is not the
// active key (KeyChanged): then no new identities are derived.
func KeyProvider(key *corekey.Key, gate func(ctx context.Context) error) Provider {
	if key == nil {
		return nil
	}
	return func(ctx context.Context, exportName string) (ResolvedIdentity, error) {
		if gate != nil {
			if err := gate(ctx); err != nil {
				return ResolvedIdentity{}, fmt.Errorf("core identities key: %w", err)
			}
		}
		hash, err := key.IdentityHash(exportName)
		if err != nil {
			return ResolvedIdentity{}, err
		}
		return ResolvedIdentity{
			Hash:       hash,
			Source:     adminv1alpha1.CoreIdentitySourceKey,
			KeyID:      key.ID(),
			Derivation: corekey.Derivation,
		}, nil
	}
}

// KeyGate returns a gate for KeyProvider that refuses while the key recorded
// in the local CoreIdentitySet differs from the mounted key.
func KeyGate(key *corekey.Key, local *LocalCoreIdentitySet) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		recorded, err := local.RecordedKeyID(ctx)
		if err != nil {
			return fmt.Errorf("cannot tell whether the key changed: %w", err)
		}
		if recorded != "" && recorded != key.ID() {
			return fmt.Errorf("the mounted key %s is not the active key %s; restore the previous key", key.ID(), recorded)
		}
		return nil
	}
}

// NewClassifier returns a Classifier that recognizes identities that were
// in fact derived from the key, or bootstrapped from --root-identities-file
// (devFile maps export names to identity hashes), even when they were
// learned from a legacy source.
func NewClassifier(key *corekey.Key, devFile map[string]string) Classifier {
	return func(exportName string, id ResolvedIdentity) ResolvedIdentity {
		if id.Source != "" && id.Source != adminv1alpha1.CoreIdentitySourceLegacy {
			return id
		}
		if key != nil {
			if derived, err := key.IdentityHash(exportName); err == nil && derived == id.Hash {
				id.Source = adminv1alpha1.CoreIdentitySourceKey
				id.KeyID = key.ID()
				id.Derivation = corekey.Derivation
				return id
			}
		}
		if hash, ok := devFile[exportName]; ok && hash == id.Hash {
			id.Source = adminv1alpha1.CoreIdentitySourceDevFile
			id.KeyID, id.Derivation = "", ""
			return id
		}
		id.Source = adminv1alpha1.CoreIdentitySourceLegacy
		return id
	}
}

// WithRecorded wraps a Classifier so that an identity whose hash matches what
// the local CoreIdentitySet already recorded for the export keeps the
// recorded source, key ID and derivation. Without it, a shard whose key
// changed would report identities it derived from the previous key as
// Legacy, because the mounted key no longer derives them. The local set is
// read once, on first use.
func WithRecorded(local *LocalCoreIdentitySet, inner Classifier) Classifier {
	var once sync.Once
	var recorded map[string]adminv1alpha1.CoreIdentity
	return func(exportName string, id ResolvedIdentity) ResolvedIdentity {
		once.Do(func() {
			set, err := local.Get(context.Background())
			if err != nil {
				return
			}
			recorded = make(map[string]adminv1alpha1.CoreIdentity, len(set.Status.Exports))
			for _, export := range set.Status.Exports {
				recorded[export.Name] = export
			}
		})
		if export, ok := recorded[exportName]; ok && export.IdentityHash == id.Hash && export.Source != "" {
			return ResolvedIdentity{Hash: id.Hash, Source: export.Source, KeyID: export.KeyID, Derivation: export.Derivation}
		}
		if inner != nil {
			return inner(exportName, id)
		}
		return id
	}
}

func legacy(hash string) ResolvedIdentity {
	return ResolvedIdentity{Hash: hash, Source: adminv1alpha1.CoreIdentitySourceLegacy}
}
