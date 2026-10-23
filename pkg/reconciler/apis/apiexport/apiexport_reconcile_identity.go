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
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"

	configshard "github.com/kcp-dev/kcp/config/shard"
	"github.com/kcp-dev/kcp/pkg/identity"
)

// reconcileIdentity assigns the export's identity and records it in the
// registry.
//
// The identity is the hash of the secret's identity key, as always. The
// registry (the APIIdentity on the cache server, created atomically by the
// first export to claim the identity) records which APIExport owns the
// identity and which others hold it, so that sharing an identity by copying
// the secret is visible and an identity whose owner is deleted passes on to
// a holder.
//
// status.identityHash is set once the identity is recorded; an identity
// that cannot be recorded is retried, with IdentityValid=False meanwhile.
func (c *controller) reconcileIdentity(ctx context.Context, apiExport *apisv1alpha2.APIExport) error {
	logger := klog.FromContext(ctx)
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
	me := adminv1alpha1.ExportReference{Cluster: clusterName.String(), Name: apiExport.Name, UID: string(apiExport.UID)}

	// claim: the first export to create the registry entry owns the identity.
	entry, err := c.getAPIIdentity(hash)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if errors.IsNotFound(err) {
		entry, err = c.createAPIIdentity(ctx, &adminv1alpha1.APIIdentity{
			ObjectMeta: metav1.ObjectMeta{
				Name:        hash,
				Annotations: map[string]string{logicalcluster.AnnotationKey: configshard.SystemShardCluster.String()},
			},
			Status: adminv1alpha1.APIIdentityStatus{Owner: me},
		})
		switch {
		case errors.IsAlreadyExists(err):
			// somebody else claimed it first, possibly on another shard and
			// not replicated to our informer yet: read it authoritatively.
			entry, err = c.getAPIIdentityLive(ctx, hash)
			if err != nil {
				return c.identityClaimFailed(apiExport, fmt.Errorf("reading APIIdentity %s: %w", hash, err))
			}
		case err != nil:
			return c.identityClaimFailed(apiExport, fmt.Errorf("claiming APIIdentity %s: %w", hash, err))
		default:
			logger.V(2).Info("claimed identity", "identity", hash)
		}
	}

	if entry.Status.Owner.Equal(me) {
		// the owner, possibly recreated under the same name (GitOps): keep
		// the registry's UID current.
		if entry.Status.Owner.UID != me.UID {
			if err := c.setAPIIdentityOwner(ctx, entry.Name, me); err != nil {
				return c.identityClaimFailed(apiExport, err)
			}
		}
		c.setIdentity(apiExport, hash)
		return nil
	}

	// a holder: whoever has the key serves the identity, as always; the
	// registry records it.
	c.setIdentity(apiExport, hash)
	return c.updateAPIIdentityHolders(ctx, entry.Name, func(entry *adminv1alpha1.APIIdentity) bool {
		return addHolder(entry, me, c.now())
	})
}

func (c *controller) setIdentity(apiExport *apisv1alpha2.APIExport, hash string) {
	apiExport.Status.IdentityHash = hash
	conditions.MarkTrue(apiExport, apisv1alpha2.APIExportIdentityValid)
}

func (c *controller) identityVerificationFailed(apiExport *apisv1alpha2.APIExport, err error) error {
	conditions.MarkFalse(apiExport, apisv1alpha2.APIExportIdentityValid, apisv1alpha2.IdentityVerificationFailedReason, conditionsv1alpha1.ConditionSeverityError, "%v", err)
	return err
}

func (c *controller) identityClaimFailed(apiExport *apisv1alpha2.APIExport, err error) error {
	conditions.MarkFalse(apiExport, apisv1alpha2.APIExportIdentityValid, apisv1alpha2.IdentityClaimFailedReason, conditionsv1alpha1.ConditionSeverityError, "%v", err)
	return err
}

// setAPIIdentityOwner records the export as the owner of the identity.
func (c *controller) setAPIIdentityOwner(ctx context.Context, name string, owner adminv1alpha1.ExportReference) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		entry, err := c.getAPIIdentityLive(ctx, name)
		if err != nil {
			return err
		}
		if entry.Status.Owner == owner {
			return nil
		}
		entry.Status.Owner = owner
		_, err = c.updateAPIIdentity(ctx, entry)
		return err
	})
}

// updateAPIIdentityHolders applies mutate to the holders of the identity and
// writes the entry when it changed anything.
func (c *controller) updateAPIIdentityHolders(ctx context.Context, name string, mutate func(*adminv1alpha1.APIIdentity) bool) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		entry, err := c.getAPIIdentityLive(ctx, name)
		if err != nil {
			return err
		}
		if !mutate(entry) {
			return nil
		}
		_, err = c.updateAPIIdentity(ctx, entry)
		return err
	})
}

func addHolder(entry *adminv1alpha1.APIIdentity, ref adminv1alpha1.ExportReference, now time.Time) bool {
	for i := range entry.Status.Holders {
		if entry.Status.Holders[i].Equal(ref) {
			if entry.Status.Holders[i].UID == ref.UID {
				return false
			}
			entry.Status.Holders[i].UID = ref.UID
			return true
		}
	}
	entry.Status.Holders = append(entry.Status.Holders, adminv1alpha1.Holder{ExportReference: ref, Since: metav1.NewTime(now)})
	return true
}
