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
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"

	"github.com/kcp-dev/logicalcluster/v3"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

func sha(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }

// identityFixture is an in-memory registry plus the secrets and exports the
// reconciler looks up.
type identityFixture struct {
	registry map[string]*adminv1alpha1.APIIdentity
	secrets  map[string]*corev1.Secret // by cluster|name
	created  int
}

func newIdentityFixture() *identityFixture {
	return &identityFixture{
		registry: map[string]*adminv1alpha1.APIIdentity{},
		secrets:  map[string]*corev1.Secret{},
	}
}

func (f *identityFixture) controller() *controller {
	return &controller{
		queue: workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]()),
		now:   func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
		getSecret: func(_ context.Context, clusterName logicalcluster.Name, _, name string) (*corev1.Secret, error) {
			if s, ok := f.secrets[clusterName.String()+"|"+name]; ok {
				return s, nil
			}
			return nil, apierrors.NewNotFound(corev1.Resource("secrets"), name)
		},
		getAPIIdentity: func(name string) (*adminv1alpha1.APIIdentity, error) {
			if e, ok := f.registry[name]; ok {
				return e.DeepCopy(), nil
			}
			return nil, apierrors.NewNotFound(adminv1alpha1.Resource("apiidentities"), name)
		},
		getAPIIdentityLive: func(_ context.Context, name string) (*adminv1alpha1.APIIdentity, error) {
			if e, ok := f.registry[name]; ok {
				return e.DeepCopy(), nil
			}
			return nil, apierrors.NewNotFound(adminv1alpha1.Resource("apiidentities"), name)
		},
		createAPIIdentity: func(_ context.Context, entry *adminv1alpha1.APIIdentity) (*adminv1alpha1.APIIdentity, error) {
			if _, ok := f.registry[entry.Name]; ok {
				return nil, apierrors.NewAlreadyExists(adminv1alpha1.Resource("apiidentities"), entry.Name)
			}
			f.created++
			f.registry[entry.Name] = entry.DeepCopy()
			return entry.DeepCopy(), nil
		},
		updateAPIIdentity: func(_ context.Context, entry *adminv1alpha1.APIIdentity) (*adminv1alpha1.APIIdentity, error) {
			f.registry[entry.Name] = entry.DeepCopy()
			return entry.DeepCopy(), nil
		},
	}
}

func (f *identityFixture) secret(cluster, name, key string) {
	f.secrets[cluster+"|"+name] = &corev1.Secret{Data: map[string][]byte{apisv1alpha1.SecretKeyAPIExportIdentity: []byte(key)}}
}

func export(cluster, name, uid string) *apisv1alpha2.APIExport {
	return &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(uid), Annotations: map[string]string{logicalcluster.AnnotationKey: cluster}},
		Spec:       apisv1alpha2.APIExportSpec{Identity: &apisv1alpha2.Identity{SecretRef: &corev1.SecretReference{Namespace: "kcp-system", Name: name}}},
	}
}

func TestReconcileIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const (
		ownerCluster  = "ws-owner"
		holderCluster = "ws-holder"
	)
	key := "the-key"
	identity := sha(key)

	t.Run("first export claims the identity and owns it", func(t *testing.T) {
		t.Parallel()
		f := newIdentityFixture()
		f.secret(ownerCluster, "networking", key)
		c := f.controller()

		ae := export(ownerCluster, "networking", "uid-1")
		require.NoError(t, c.reconcileIdentity(ctx, ae))

		require.Equal(t, identity, ae.Status.IdentityHash)
		require.True(t, conditions.IsTrue(ae, apisv1alpha2.APIExportIdentityValid))
		require.Equal(t, 1, f.created)
		require.Equal(t, adminv1alpha1.ExportReference{Cluster: ownerCluster, Name: "networking", UID: "uid-1"}, f.registry[identity].Status.Owner)
	})

	t.Run("a recreated owner keeps the identity and refreshes its UID", func(t *testing.T) {
		t.Parallel()
		f := newIdentityFixture()
		f.secret(ownerCluster, "networking", key)
		f.registry[identity] = &adminv1alpha1.APIIdentity{ObjectMeta: metav1.ObjectMeta{Name: identity}, Status: adminv1alpha1.APIIdentityStatus{Owner: adminv1alpha1.ExportReference{Cluster: ownerCluster, Name: "networking", UID: "old"}}}
		c := f.controller()

		ae := export(ownerCluster, "networking", "new")
		require.NoError(t, c.reconcileIdentity(ctx, ae))

		require.Equal(t, identity, ae.Status.IdentityHash)
		require.Equal(t, "new", f.registry[identity].Status.Owner.UID)
		require.Equal(t, 0, f.created, "no second claim")
	})

	t.Run("a holder of the key shares the identity and is recorded", func(t *testing.T) {
		t.Parallel()
		f := newIdentityFixture()
		f.secret(holderCluster, "networking-v2", key)
		f.registry[identity] = &adminv1alpha1.APIIdentity{ObjectMeta: metav1.ObjectMeta{Name: identity}, Status: adminv1alpha1.APIIdentityStatus{Owner: adminv1alpha1.ExportReference{Cluster: ownerCluster, Name: "networking"}}}
		c := f.controller()

		ae := export(holderCluster, "networking-v2", "h")
		require.NoError(t, c.reconcileIdentity(ctx, ae))

		require.Equal(t, identity, ae.Status.IdentityHash, "copying the secret keeps working as today")
		require.True(t, conditions.IsTrue(ae, apisv1alpha2.APIExportIdentityValid))
		require.Len(t, f.registry[identity].Status.Holders, 1)
		require.Equal(t, holderCluster, f.registry[identity].Status.Holders[0].Cluster)

		require.NoError(t, c.reconcileIdentity(ctx, ae))
		require.Len(t, f.registry[identity].Status.Holders, 1, "recorded once")
	})

	t.Run("a secret edited in place fails verification", func(t *testing.T) {
		t.Parallel()
		f := newIdentityFixture()
		f.secret(ownerCluster, "networking", "another-key")
		c := f.controller()

		ae := export(ownerCluster, "networking", "o")
		ae.Status.IdentityHash = identity
		require.Error(t, c.reconcileIdentity(ctx, ae))

		cond := conditions.Get(ae, apisv1alpha2.APIExportIdentityValid)
		require.NotNil(t, cond)
		require.Equal(t, apisv1alpha2.IdentityVerificationFailedReason, cond.Reason)
		require.Equal(t, 0, f.created)
	})
}
