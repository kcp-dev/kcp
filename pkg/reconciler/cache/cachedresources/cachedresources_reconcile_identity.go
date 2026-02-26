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

package cachedresources

import (
	"context"
	"crypto/sha256"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

// identity creates identity secret for the published resource and computes hash of the secret.
type identity struct {
	ensureSecretNamespaceExists func(ctx context.Context, clusterName logicalcluster.Name, defaultSecretNamespace string)
	getSecret                   func(ctx context.Context, clusterName logicalcluster.Name, namespace, name string) (*corev1.Secret, error)
	createIdentitySecret        func(ctx context.Context, clusterName logicalcluster.Path, defaultSecretNamespace, name string) error

	secretNamespace string
}

func (r *identity) reconcile(ctx context.Context, cachedResource *cachev1alpha1.CachedResource) (reconcileStatus, error) {
	if !cachedResource.DeletionTimestamp.IsZero() {
		return reconcileStatusContinue, nil
	}
	identity := cachedResource.Spec.Identity
	if identity == nil {
		identity = &cachev1alpha1.Identity{}
	}

	clusterName := logicalcluster.From(cachedResource)

	if identity.SecretRef == nil {
		r.ensureSecretNamespaceExists(ctx, clusterName, r.secretNamespace)

		// See if the generated secret already exists (for whatever reason)
		_, err := r.getSecret(ctx, clusterName, r.secretNamespace, cachedResource.Name)
		if err != nil && !errors.IsNotFound(err) {
			return reconcileStatusStop, fmt.Errorf("error checking if APIExport %s|%s identity secret %s|%s/%s exists: %w",
				clusterName, cachedResource.Name,
				clusterName, r.secretNamespace, cachedResource.Name,
				err,
			)
		}
		if errors.IsNotFound(err) {
			if err := r.createIdentitySecret(ctx, clusterName.Path(), r.secretNamespace, cachedResource.Name); err != nil {
				conditions.MarkFalse(
					cachedResource,
					cachev1alpha1.CachedResourceIdentityValid,
					cachev1alpha1.IdentityGenerationFailedReason,
					conditionsv1alpha1.ConditionSeverityError,
					"Error creating identity secret: %v",
					err,
				)

				return reconcileStatusStop, err
			}
		}

		identity.SecretRef = &corev1.SecretReference{
			Namespace: r.secretNamespace,
			Name:      cachedResource.Name,
		}

		cachedResource.Spec.Identity = identity

		// Record the spec change. A future iteration will store the hash in status.
		return reconcileStatusStopAndRequeue, nil
	}

	// Ref exists - make sure it's valid
	if err := r.updateOrVerifyIdentitySecretHash(ctx, clusterName, cachedResource, r.secretNamespace); err != nil {
		conditions.MarkFalse(
			cachedResource,
			cachev1alpha1.CachedResourceIdentityValid,
			cachev1alpha1.IdentityVerificationFailedReason,
			conditionsv1alpha1.ConditionSeverityError,
			"%v",
			err,
		)

		return reconcileStatusStop, err
	}

	conditions.MarkTrue(cachedResource, cachev1alpha1.CachedResourceIdentityValid)

	return reconcileStatusContinue, nil
}

func (r *identity) updateOrVerifyIdentitySecretHash(ctx context.Context, clusterName logicalcluster.Name, cachedResource *cachev1alpha1.CachedResource, defaultSecretNamespace string) error {
	secret, err := r.getSecret(ctx, clusterName, cachedResource.Spec.Identity.SecretRef.Namespace, cachedResource.Spec.Identity.SecretRef.Name)
	if err != nil {
		return err
	}

	hash, err := IdentityHash(secret)
	if err != nil {
		return err
	}

	if cachedResource.Status.IdentityHash == "" {
		cachedResource.Status.IdentityHash = hash
	}

	if cachedResource.Status.IdentityHash != hash {
		return fmt.Errorf("hash mismatch: identity secret hash %q must match status.identityHash %q", hash, cachedResource.Status.IdentityHash)
	}

	return nil
}

// TODO: This is copy from apiexport controller. We should move it to a shared location.
func IdentityHash(secret *corev1.Secret) (string, error) {
	key := secret.Data[apisv1alpha1.SecretKeyAPIExportIdentity]
	if len(key) == 0 {
		return "", fmt.Errorf("secret is missing data.%s", apisv1alpha1.SecretKeyAPIExportIdentity)
	}

	hashBytes := sha256.Sum256(key)
	hash := fmt.Sprintf("%x", hashBytes)
	return hash, nil
}
