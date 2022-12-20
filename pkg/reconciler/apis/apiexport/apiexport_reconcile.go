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

	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	virtualworkspacesoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	"github.com/kcp-dev/kcp/pkg/logging"
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
			if err := c.createIdentitySecret(ctx, clusterName.Path(), apiExport.Name); err != nil {
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

	// TODO(sttts): reactivate this with multi-shard support eventually
	/*
		// check if any APIBindings are bound to this APIExport. If so, add a virtualworkspaceURL
		apiBindings, err := c.getAPIBindingsForAPIExport(clusterName, apiExport.Name)
		if err != nil {
			return fmt.Errorf("error checking for APIBindings with APIExport %s|%s: %w", clusterName, apiExport.Name, err)
		}

		// If there are no bindings, then we can't create a URL yet.
		if len(apiBindings) == 0 {
			return nil
		}
	*/

	if err := c.updateVirtualWorkspaceURLs(ctx, apiExport); err != nil {
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
	logger := klog.FromContext(ctx)
	ctx = klog.NewContext(ctx, logger)
	if _, err := c.getNamespace(clusterName, c.secretNamespace); errors.IsNotFound(err) {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:        c.secretNamespace,
				Annotations: map[string]string{logicalcluster.AnnotationKey: clusterName.String()},
			},
		}
		logger = logging.WithObject(logger, ns)
		if err := c.createNamespace(ctx, clusterName.Path(), ns); err != nil && !errors.IsAlreadyExists(err) {
			logger.Error(err, "error creating namespace for APIExport secret identities")
			// Keep going - maybe things will work. If the secret creation fails, we'll make sure to set a condition.
		}
	}
}

func (c *controller) createIdentitySecret(ctx context.Context, clusterName logicalcluster.Path, apiExportName string) error {
	secret, err := GenerateIdentitySecret(ctx, c.secretNamespace, apiExportName)
	if err != nil {
		return err
	}
	secret.Annotations[logicalcluster.AnnotationKey] = clusterName.String()

	logger := logging.WithObject(klog.FromContext(ctx), secret)
	ctx = klog.NewContext(ctx, logger)
	logger.V(2).Info("creating identity secret")
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

func (c *controller) updateVirtualWorkspaceURLs(ctx context.Context, apiExport *apisv1alpha1.APIExport) error {
	logger := klog.FromContext(ctx)
	shards, err := c.listShards()
	if err != nil {
		return fmt.Errorf("error listing Shards: %w", err)
	}

	desiredURLs := sets.NewString()
	for _, shard := range shards {
		logger = logging.WithObject(logger, shard)
		if shard.Spec.VirtualWorkspaceURL == "" {
			continue
		}

		u, err := url.Parse(shard.Spec.VirtualWorkspaceURL)
		if err != nil {
			// Should never happen
			logger.Error(
				err, "error parsing Shard.Spec.VirtualWorkspaceURL",
				"VirtualWorkspaceURL", shard.Spec.VirtualWorkspaceURL,
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
