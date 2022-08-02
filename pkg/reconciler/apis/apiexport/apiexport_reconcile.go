/*
Copyright 2022 The KCP Authors.

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

package apiexport

import (
	"context"
	"fmt"
	"net/url"
	"path"

	"github.com/kcp-dev/logicalcluster/v2"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	virtualworkspacesoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	apiexportbuilder "github.com/kcp-dev/kcp/pkg/virtual/apiexport/builder"
)

func (c *controller) reconcile(ctx context.Context, apiExport *apisv1alpha1.APIExport) error {
	identity := apiExport.Spec.Identity
	if identity == nil {
		identity = &apisv1alpha1.Identity{}
	}

	clusterName := logicalcluster.From(apiExport)

	if identity.SecretRef == nil {
		c.ensureSecretNamespaceExists(ctx, clusterName)

		// See if the generated secret already exists (for whatever reason)
		_, err := c.getSecret(ctx, clusterName, c.secretNamespace, apiExport.Name)
		if err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("error checking if APIExport %s|%s identity secret %s|%s/%s exists: %w",
				clusterName, apiExport.Name,
				clusterName, c.secretNamespace, apiExport.Name,
				err,
			)
		}
		if errors.IsNotFound(err) {
			if err := c.createIdentitySecret(ctx, clusterName, apiExport.Name); err != nil {
				conditions.MarkFalse(
					apiExport,
					apisv1alpha1.APIExportIdentityValid,
					apisv1alpha1.IdentityGenerationFailedReason,
					conditionsv1alpha1.ConditionSeverityError,
					"Error creating identity secret: %v",
					err,
				)

				return err
			}
		}

		identity.SecretRef = &corev1.SecretReference{
			Namespace: c.secretNamespace,
			Name:      apiExport.Name,
		}

		apiExport.Spec.Identity = identity

		// Record the spec change. A future iteration will store the hash in status.
		return nil
	}

	// Ref exists - make sure it's valid
	if err := c.updateOrVerifyIdentitySecretHash(ctx, clusterName, apiExport); err != nil {
		conditions.MarkFalse(
			apiExport,
			apisv1alpha1.APIExportIdentityValid,
			apisv1alpha1.IdentityVerificationFailedReason,
			conditionsv1alpha1.ConditionSeverityError,
			err.Error(),
		)
	}

	if err := c.updateVirtualWorkspaceURLs(apiExport); err != nil {
		conditions.MarkFalse(
			apiExport,
			apisv1alpha1.APIExportVirtualWorkspaceURLsReady,
			apisv1alpha1.ErrorGeneratingURLsReason,
			conditionsv1alpha1.ConditionSeverityError,
			err.Error(),
		)
	}

	return nil
}

func (c *controller) ensureSecretNamespaceExists(ctx context.Context, clusterName logicalcluster.Name) {
	if _, err := c.getNamespace(clusterName, c.secretNamespace); errors.IsNotFound(err) {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: c.secretNamespace,
			},
		}
		if err := c.createNamespace(ctx, clusterName, ns); err != nil && !errors.IsAlreadyExists(err) {
			klog.Errorf("Error creating namespace %q in cluster %q for APIExport secret identities: %v", c.secretNamespace, clusterName, err)
			// Keep going - maybe things will work. If the secret creation fails, we'll make sure to set a condition.
		}
	}
}

func (c *controller) createIdentitySecret(ctx context.Context, clusterName logicalcluster.Name, apiExportName string) error {
	secret, err := GenerateIdentitySecret(c.secretNamespace, apiExportName)
	if err != nil {
		return err
	}

	klog.V(2).Infof("Creating identity secret %s|%s/%s for APIExport %s|%s", clusterName, c.secretNamespace, secret.Name, clusterName, apiExportName)
	if err := c.createSecret(ctx, clusterName, secret); err != nil {
		return err
	}

	return nil
}

func (c *controller) updateOrVerifyIdentitySecretHash(ctx context.Context, clusterName logicalcluster.Name, apiExport *apisv1alpha1.APIExport) error {
	secret, err := c.getSecret(ctx, clusterName, apiExport.Spec.Identity.SecretRef.Namespace, apiExport.Spec.Identity.SecretRef.Name)
	if err != nil {
		return err
	}

	hash, err := IdentityHash(secret)
	if err != nil {
		return err
	}

	if apiExport.Status.IdentityHash == "" {
		apiExport.Status.IdentityHash = hash
	}

	if apiExport.Status.IdentityHash != hash {
		return fmt.Errorf("hash mismatch: identity secret hash %q must match status.identityHash %q", hash, apiExport.Status.IdentityHash)
	}

	conditions.MarkTrue(apiExport, apisv1alpha1.APIExportIdentityValid)

	return nil
}

func (c *controller) updateVirtualWorkspaceURLs(apiExport *apisv1alpha1.APIExport) error {
	clusterWorkspaceShards, err := c.listClusterWorkspaceShards()
	if err != nil {
		return fmt.Errorf("error listing ClusterWorkspaceShards: %w", err)
	}

	desiredURLs := sets.NewString()
	for _, clusterWorkspaceShard := range clusterWorkspaceShards {
		if clusterWorkspaceShard.Spec.VirtualWorkspaceURL == "" {
			continue
		}

		u, err := url.Parse(clusterWorkspaceShard.Spec.VirtualWorkspaceURL)
		if err != nil {
			// Should never happen
			klog.Errorf(
				"Error parsing ClusterWorkspaceShard %s|%s spec.externalURL %q: %v",
				logicalcluster.From(clusterWorkspaceShard),
				clusterWorkspaceShard.Name,
				clusterWorkspaceShard.Spec.VirtualWorkspaceURL,
			)

			continue
		}

		u.Path = path.Join(
			u.Path,
			virtualworkspacesoptions.DefaultRootPathPrefix,
			apiexportbuilder.VirtualWorkspaceName,
			logicalcluster.From(apiExport).String(),
			apiExport.Name,
		)

		desiredURLs.Insert(u.String())
	}

	apiExport.Status.VirtualWorkspaces = nil

	for _, u := range desiredURLs.List() {
		apiExport.Status.VirtualWorkspaces = append(apiExport.Status.VirtualWorkspaces, apisv1alpha1.VirtualWorkspace{
			URL: u,
		})
	}

	conditions.MarkTrue(apiExport, apisv1alpha1.APIExportVirtualWorkspaceURLsReady)

	return nil
}
