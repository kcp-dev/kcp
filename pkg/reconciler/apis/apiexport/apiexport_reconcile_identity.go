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

package apiexport

import (
	"context"
	"fmt"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"

	"github.com/kcp-dev/kcp/pkg/identity"
)

// reconcileIdentity assigns the export's identity: the hash of the secret's
// identity key. Two exports whose secrets carry the same key have the same
// identity, which is how an identity is shared; who owns it and who holds it
// is derived from the exports themselves (see pkg/apiidentity), not
// recorded anywhere.
//
// Once set, status.identityHash is immutable: a secret edited to another
// key fails verification instead of changing the identity.
func (c *controller) reconcileIdentity(ctx context.Context, apiExport *apisv1alpha2.APIExport) error {
	clusterName := logicalcluster.From(apiExport)

	secret, err := c.getSecret(ctx, clusterName, apiExport.Spec.Identity.SecretRef.Namespace, apiExport.Spec.Identity.SecretRef.Name)
	if err != nil {
		return c.identityVerificationFailed(apiExport, err)
	}
	hash, err := identity.IdentityHash(secret)
	if err != nil {
		return c.identityVerificationFailed(apiExport, err)
	}
	if apiExport.Status.IdentityHash != "" && apiExport.Status.IdentityHash != hash {
		return c.identityVerificationFailed(apiExport, fmt.Errorf("hash mismatch: identity secret hash %q must match status.identityHash %q", hash, apiExport.Status.IdentityHash))
	}

	apiExport.Status.IdentityHash = hash
	conditions.MarkTrue(apiExport, apisv1alpha2.APIExportIdentityValid)
	return nil
}

func (c *controller) identityVerificationFailed(apiExport *apisv1alpha2.APIExport, err error) error {
	conditions.MarkFalse(apiExport, apisv1alpha2.APIExportIdentityValid, apisv1alpha2.IdentityVerificationFailedReason, conditionsv1alpha1.ConditionSeverityError, "%v", err)
	return err
}
