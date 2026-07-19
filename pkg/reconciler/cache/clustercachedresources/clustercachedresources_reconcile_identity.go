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

package clustercachedresources

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"

	"github.com/kcp-dev/logicalcluster/v3"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"

	"github.com/kcp-dev/kcp/pkg/identity"
)

// identityReconciler creates identity secret for the published resource and computes hash of the secret.
type identityReconciler struct {
	ensureSecretNamespaceExists func(ctx context.Context, clusterName logicalcluster.Name, defaultSecretNamespace string)
	getSecret                   func(ctx context.Context, clusterName logicalcluster.Name, namespace, name string) (*corev1.Secret, error)
	createIdentitySecret        func(ctx context.Context, clusterName logicalcluster.Path, defaultSecretNamespace, name string) error

	secretNamespace string
}

func (r *identityReconciler) reconcile(ctx context.Context, clusterCachedResource *cachev1alpha1.ClusterCachedResource) (reconcileStatus, error) {
	if !clusterCachedResource.DeletionTimestamp.IsZero() {
		return reconcileStatusContinue, nil
	}
	identity := clusterCachedResource.Spec.Identity
	if identity == nil {
		identity = &cachev1alpha1.Identity{}
	}

	clusterName := logicalcluster.From(clusterCachedResource)

	if identity.SecretRef == nil {
		r.ensureSecretNamespaceExists(ctx, clusterName, r.secretNamespace)

		// See if the generated secret already exists (for whatever reason)
		_, err := r.getSecret(ctx, clusterName, r.secretNamespace, clusterCachedResource.Name)
		if err != nil && !errors.IsNotFound(err) {
			return reconcileStatusStop, fmt.Errorf("error checking if APIExport %s|%s identity secret %s|%s/%s exists: %w",
				clusterName, clusterCachedResource.Name,
				clusterName, r.secretNamespace, clusterCachedResource.Name,
				err,
			)
		}
		if errors.IsNotFound(err) {
			if err := r.createIdentitySecret(ctx, clusterName.Path(), r.secretNamespace, clusterCachedResource.Name); err != nil {
				conditions.MarkFalse(
					clusterCachedResource,
					cachev1alpha1.ClusterCachedResourceIdentityValid,
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
			Name:      clusterCachedResource.Name,
		}

		clusterCachedResource.Spec.Identity = identity

		// Record the spec change. A future iteration will store the hash in status.
		return reconcileStatusStopAndRequeue, nil
	}

	// Ref exists - make sure it's valid
	if err := r.updateOrVerifyIdentitySecretHash(ctx, clusterName, clusterCachedResource, r.secretNamespace); err != nil {
		conditions.MarkFalse(
			clusterCachedResource,
			cachev1alpha1.ClusterCachedResourceIdentityValid,
			cachev1alpha1.IdentityVerificationFailedReason,
			conditionsv1alpha1.ConditionSeverityError,
			"%v",
			err,
		)

		return reconcileStatusStop, err
	}

	if !conditions.IsTrue(clusterCachedResource, cachev1alpha1.ClusterCachedResourceIdentityValid) {
		conditions.MarkTrue(clusterCachedResource, cachev1alpha1.ClusterCachedResourceIdentityValid)
	}

	return reconcileStatusContinue, nil
}

func (r *identityReconciler) updateOrVerifyIdentitySecretHash(ctx context.Context, clusterName logicalcluster.Name, clusterCachedResource *cachev1alpha1.ClusterCachedResource, defaultSecretNamespace string) error {
	secret, err := r.getSecret(ctx, clusterName, clusterCachedResource.Spec.Identity.SecretRef.Namespace, clusterCachedResource.Spec.Identity.SecretRef.Name)
	if err != nil {
		return err
	}

	hash, err := identity.IdentityHash(secret)
	if err != nil {
		return err
	}

	if clusterCachedResource.Status.IdentityHash == "" {
		clusterCachedResource.Status.IdentityHash = hash
	}

	if clusterCachedResource.Status.IdentityHash != hash {
		return fmt.Errorf("hash mismatch: identity secret hash %q must match status.identityHash %q", hash, clusterCachedResource.Status.IdentityHash)
	}

	return nil
}
