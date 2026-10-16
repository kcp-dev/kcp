/*
Copyright 2025 The kcp Authors.

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

package rootidentities

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"

	"sigs.k8s.io/yaml"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"

	confighelpers "github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/pkg/errgroup"
	"github.com/kcp-dev/kcp/pkg/identity"
	"github.com/kcp-dev/kcp/pkg/identity/corekey"
)

//go:embed *.yaml
var fs embed.FS

// SecretNamespace is the namespace in the root workspace that holds the
// identity secrets of the core APIExports.
const SecretNamespace = "kcp-system"

// Identities is the structure we expect to receive
// from --root-identities-file in YAML format.
type Identities struct {
	Identities []Identity `json:"identities"`
}

// Identity is one entry of --root-identities-file: the identity key of an
// APIExport in the root workspace.
type Identity struct {
	Export   string `json:"export"`
	Identity string `json:"identity"`
}

// Parse parses the content of --root-identities-file.
func Parse(bs []byte) (*Identities, error) {
	var identities Identities
	if err := yaml.UnmarshalStrict(bs, &identities); err != nil {
		return nil, fmt.Errorf("failed to parse bootstrap identities: %w", err)
	}
	return &identities, nil
}

// Hashes returns the identity hash of every entry, by APIExport name.
func (ids *Identities) Hashes() map[string]string {
	hashes := make(map[string]string, len(ids.Identities))
	for _, id := range ids.Identities {
		sum := sha256.Sum256([]byte(id.Identity))
		hashes[id.Export] = hex.EncodeToString(sum[:])
	}
	return hashes
}

// Bootstrap creates resources in this package by continuously retrying the list.
// This is blocking, i.e. it only returns (with error) when the context is closed or with nil when
// the bootstrapping is successfully completed.
//
// Existing identity secrets are never replaced. Use Verify afterwards to
// find entries that disagree with an existing identity.
func Bootstrap(
	ctx context.Context,
	rootDiscoveryClient discovery.DiscoveryInterface,
	rootDynamicClient dynamic.Interface,
	kubeClient kubernetes.Interface,
	bootstrapIdentitiesBytes []byte,
) error {
	identities, err := Parse(bootstrapIdentitiesBytes)
	if err != nil {
		return err
	}

	g := errgroup.WithContext(ctx)
	for _, identity := range identities.Identities {
		g.Go(func(ctx context.Context) error {
			if err := confighelpers.Bootstrap(ctx, kubeClient.Discovery(), rootDynamicClient, nil, fs, confighelpers.ReplaceOption(
				"IDENTITY_APIEXPORT", identity.Export,
				"IDENTITY_KEY", identity.Identity,
			)); err != nil {
				return fmt.Errorf("failed to bootstrap identity secret for %s: %v", identity.Export, err)
			}
			return nil
		})
	}

	return g.Wait()
}

// Verify checks every entry of --root-identities-file against the identity
// secret that exists in the root workspace. An entry whose identity differs
// from the existing one is a configuration error: identities in use never
// change. The error names the APIExport and both hashes.
func Verify(ctx context.Context, kubeClient kubernetes.Interface, identities *Identities) error {
	var errs []error
	want := identities.Hashes()
	for _, id := range identities.Identities {
		secret, err := kubeClient.CoreV1().Secrets(SecretNamespace).Get(ctx, id.Export, metav1.GetOptions{})
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to get identity secret for %s: %w", id.Export, err))
			continue
		}
		existing, err := identity.IdentityHash(secret)
		if err != nil {
			errs = append(errs, fmt.Errorf("identity secret for %s: %w", id.Export, err))
			continue
		}
		if existing != want[id.Export] {
			errs = append(errs, fmt.Errorf("--root-identities-file entry for %s has identity hash %s but the existing identity is %s; identities in use never change", id.Export, want[id.Export], existing))
		}
	}
	return utilerrors.NewAggregate(errs)
}

// SeedFromKey creates the identity secret of every named APIExport in the
// root workspace from the core identities key, unless it already exists.
// Existing secrets are never replaced: the key only fills in missing
// identities.
func SeedFromKey(ctx context.Context, kubeClient kubernetes.Interface, key *corekey.Key, exportNames []string) error {
	logger := klog.FromContext(ctx)
	g := errgroup.WithContext(ctx)
	for _, name := range exportNames {
		g.Go(func(ctx context.Context) error {
			identityKey, err := key.IdentityKey(name)
			if err != nil {
				return err
			}
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: SecretNamespace,
					Name:      name,
				},
				StringData: map[string]string{
					apisv1alpha1.SecretKeyAPIExportIdentity: identityKey,
				},
			}
			_, err = kubeClient.CoreV1().Secrets(SecretNamespace).Create(ctx, secret, metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(err) {
				logger.V(2).Info("identity secret exists, not seeding it from the core identities key", "export", name)
				return nil
			}
			if err != nil {
				return fmt.Errorf("failed to seed identity secret for %s from the core identities key: %w", name, err)
			}
			logger.Info("seeded identity secret from the core identities key", "export", name, "keyID", key.ID())
			return nil
		})
	}
	return g.Wait()
}
