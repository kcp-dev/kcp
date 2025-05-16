/*
Copyright 2025 The KCP Authors.

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

package publishedresources

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"

	"github.com/kcp-dev/logicalcluster/v3"

	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
)

// identity creates identity secret for the published resource and computes hash of the secret.
type identity struct {
	ensureSecretNamespaceExists      func(ctx context.Context, clusterName logicalcluster.Name)
	getSecret                        func(ctx context.Context, clusterName logicalcluster.Name, namespace, name string) (*corev1.Secret, error)
	createIdentitySecret             func(ctx context.Context, clusterName logicalcluster.Path, name string) error
	updateOrVerifyIdentitySecretHash func(ctx context.Context, clusterName logicalcluster.Name, publishedResource *cachev1alpha1.PublishedResource) error

	secretNamespace string
}

func (r *identity) reconcile(ctx context.Context, publishedResource *cachev1alpha1.PublishedResource) (reconcileStatus, error) {
	if !publishedResource.DeletionTimestamp.IsZero() {
		return reconcileStatusContinue, nil
	}
	identity := publishedResource.Spec.Identity
	if identity == nil {
		identity = &cachev1alpha1.Identity{}
	}

	clusterName := logicalcluster.From(publishedResource)

	if identity.SecretRef == nil {
		r.ensureSecretNamespaceExists(ctx, clusterName)

		// See if the generated secret already exists (for whatever reason)
		_, err := r.getSecret(ctx, clusterName, r.secretNamespace, publishedResource.Name)
		if err != nil && !errors.IsNotFound(err) {
			return reconcileStatusStop, fmt.Errorf("error checking if APIExport %s|%s identity secret %s|%s/%s exists: %w",
				clusterName, publishedResource.Name,
				clusterName, r.secretNamespace, publishedResource.Name,
				err,
			)
		}
		if errors.IsNotFound(err) {
			if err := r.createIdentitySecret(ctx, clusterName.Path(), publishedResource.Name); err != nil {
				conditions.MarkFalse(
					publishedResource,
					cachev1alpha1.PublishedResourceIdentityValid,
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
			Name:      publishedResource.Name,
		}

		publishedResource.Spec.Identity = identity

		// Record the spec change. A future iteration will store the hash in status.
		return reconcileStatusStopAndRequeue, nil
	}

	// Ref exists - make sure it's valid
	if err := r.updateOrVerifyIdentitySecretHash(ctx, clusterName, publishedResource); err != nil {
		conditions.MarkFalse(
			publishedResource,
			cachev1alpha1.PublishedResourceIdentityValid,
			cachev1alpha1.IdentityVerificationFailedReason,
			conditionsv1alpha1.ConditionSeverityError,
			"%v",
			err,
		)

		return reconcileStatusStop, err
	}

	return reconcileStatusContinue, nil
}
