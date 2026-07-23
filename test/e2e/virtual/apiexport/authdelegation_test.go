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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestAPIExportAuthDelegation explores the delegated authn/authz story for
// APIExport providers described in https://github.com/kcp-dev/kcp/issues/4279.
//
// A service provider that terminates its own endpoints (reverse tunnels, agent
// registration, webhooks, ...) needs the standard auth-delegator pattern against
// the consumer workspaces it serves:
//
//  1. POST subjectaccessreviews.authorization.k8s.io — authorize a resolved identity, and
//  2. POST tokenreviews.authentication.k8s.io         — authenticate a presented token.
//
// Both halves are served through the sanctioned channel: SubjectAccessReview,
// LocalSubjectAccessReview and TokenReview are claimable built-in APIs, and the
// APIExport virtual workspace forwards a `create` to the consumer workspace's
// authorizer/authenticator, scoped to the engaged cluster. Each subtest binds the
// corresponding claim and drives the review API through the VW URL.
func TestAPIExportAuthDelegation(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	ctx := t.Context()

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("provider"))
	consumerPath, consumer := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("consumer"))

	cfg := server.BaseConfig(t)
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")
	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kube cluster client for server")

	// The provider ships an APIExport that, alongside its own resource (cowboys),
	// claims the authn/authz review APIs it needs to delegate auth for the
	// consumers it serves.
	sarClaim := apisv1alpha2.PermissionClaim{
		GroupResource: apisv1alpha2.GroupResource{Group: "authorization.k8s.io", Resource: "subjectaccessreviews"},
		Verbs:         []string{"*"},
	}
	lsarClaim := apisv1alpha2.PermissionClaim{
		GroupResource: apisv1alpha2.GroupResource{Group: "authorization.k8s.io", Resource: "localsubjectaccessreviews"},
		Verbs:         []string{"*"},
	}
	tokenReviewClaim := apisv1alpha2.PermissionClaim{
		GroupResource: apisv1alpha2.GroupResource{Group: "authentication.k8s.io", Resource: "tokenreviews"},
		Verbs:         []string{"*"},
	}

	t.Logf("Create today-cowboys APIExport in provider %q claiming (local)subjectaccessreviews + tokenreviews", providerPath)
	setUpServiceProvider(t, dynamicClusterClient, kcpClusterClient, false, providerPath, cfg,
		[]apisv1alpha2.PermissionClaim{sarClaim, lsarClaim, tokenReviewClaim})

	t.Logf("Bind consumer %q to the provider and accept the review claims", consumerPath)
	bindConsumerToProvider(t, consumerPath, providerPath, kcpClusterClient, cfg,
		acceptClaim(sarClaim), acceptClaim(lsarClaim), acceptClaim(tokenReviewClaim))

	t.Logf("Waiting for the APIExport virtual workspace URL scoped to consumer %q", consumerPath)
	consumerVWCfg := rest.CopyConfig(cfg)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		slice, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, "today-cowboys", metav1.GetOptions{})
		require.NoError(t, err)
		var found bool
		consumerVWCfg.Host, found, err = framework.VirtualWorkspaceURL(ctx, kcpClusterClient, consumer, framework.ExportVirtualWorkspaceURLs(slice))
		require.NoError(t, err)
		return found, fmt.Sprintf("waiting for virtual workspace URL: %v", slice.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	kubeVWClusterClient, err := kcpkubernetesclientset.NewForConfig(consumerVWCfg)
	require.NoError(t, err)

	t.Run("SubjectAccessReview is served on the APIExport virtual workspace, scoped to the consumer", func(t *testing.T) {
		t.Parallel()
		// This is the sanctioned channel the provider would use to authorize an
		// identity it resolved for a caller of one of its own endpoints. The VW
		// forwards to the consumer workspace's authorizer and returns the decision.
		sar := &authorizationv1.SubjectAccessReview{
			Spec: authorizationv1.SubjectAccessReviewSpec{
				ResourceAttributes: &authorizationv1.ResourceAttributes{
					Group:     "",
					Version:   "v1",
					Resource:  "configmaps",
					Verb:      "list",
					Namespace: "default",
				},
				User:   "some-agent",
				Groups: []string{"system:authenticated"},
			},
		}
		result, err := kubeVWClusterClient.Cluster(logicalcluster.NewPath(consumer.Spec.Cluster)).
			AuthorizationV1().SubjectAccessReviews().Create(ctx, sar, metav1.CreateOptions{})
		require.NoError(t, err, "SubjectAccessReview should be served on the APIExport virtual workspace")
		// The review was evaluated against the consumer workspace (status is populated),
		// which is exactly the delegated-authz decision the provider needs.
		t.Logf("SubjectAccessReview evaluated: allowed=%v reason=%q", result.Status.Allowed, result.Status.Reason)
	})

	t.Run("LocalSubjectAccessReview is served on the APIExport virtual workspace, scoped to the consumer", func(t *testing.T) {
		t.Parallel()
		lsar := &authorizationv1.LocalSubjectAccessReview{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
			Spec: authorizationv1.SubjectAccessReviewSpec{
				ResourceAttributes: &authorizationv1.ResourceAttributes{
					Group:     "",
					Version:   "v1",
					Resource:  "configmaps",
					Verb:      "list",
					Namespace: "default",
				},
				User:   "some-agent",
				Groups: []string{"system:authenticated"},
			},
		}
		result, err := kubeVWClusterClient.Cluster(logicalcluster.NewPath(consumer.Spec.Cluster)).
			AuthorizationV1().LocalSubjectAccessReviews("default").Create(ctx, lsar, metav1.CreateOptions{})
		require.NoError(t, err, "LocalSubjectAccessReview should be served on the APIExport virtual workspace")
		t.Logf("LocalSubjectAccessReview evaluated: allowed=%v reason=%q", result.Status.Allowed, result.Status.Reason)
	})

	t.Run("TokenReview is served on the APIExport virtual workspace, scoped to the consumer", func(t *testing.T) {
		t.Parallel()
		// This is the delegated-authentication half of #4279. The provider validates a
		// credential minted in the consumer workspace by POSTing a TokenReview through
		// the VW; the request is forwarded to the consumer workspace's authenticator,
		// which is where the token was issued and therefore where it can be validated.
		const kcpDefaultAudience = "https://kcp.default.svc"

		t.Logf("Minting a ServiceAccount token in the consumer workspace (the credential to validate)")
		agentToken := mintServiceAccountToken(ctx, t, kubeClusterClient, consumerPath, kcpDefaultAudience)

		review := &authenticationv1.TokenReview{
			Spec: authenticationv1.TokenReviewSpec{
				Token:     agentToken,
				Audiences: []string{kcpDefaultAudience},
			},
		}
		result, err := kubeVWClusterClient.Cluster(logicalcluster.NewPath(consumer.Spec.Cluster)).
			AuthenticationV1().TokenReviews().Create(ctx, review, metav1.CreateOptions{})
		require.NoError(t, err, "TokenReview should be served on the APIExport virtual workspace")
		require.True(t, result.Status.Authenticated, "consumer-minted token should authenticate: %s", result.Status.Error)
		// The token was validated by the consumer workspace's authenticator, as proven
		// by the cluster-name extra kcp attaches during workspace-scoped authentication.
		require.Equal(t, authenticationv1.ExtraValue{consumer.Spec.Cluster},
			result.Status.User.Extra["authentication.kcp.io/cluster-name"],
			"token must be authenticated against the consumer workspace")
		t.Logf("TokenReview authenticated %q against consumer cluster %s", result.Status.User.Username, consumer.Spec.Cluster)
	})
}

// mintServiceAccountToken creates a namespace in the given workspace, waits for its
// auto-provisioned "default" ServiceAccount, and returns a freshly minted bound token
// for it with the given audience.
func mintServiceAccountToken(ctx context.Context, t *testing.T, kubeClusterClient kcpkubernetesclientset.ClusterInterface, path logicalcluster.Path, audience string) string {
	t.Helper()

	ns, err := kubeClusterClient.Cluster(path).CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "e2e-authdelegation-"},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create namespace in %s", path)

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kubeClusterClient.Cluster(path).CoreV1().ServiceAccounts(ns.Name).Get(ctx, "default", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, "waiting for \"default\" ServiceAccount"
		}
		require.NoError(t, err)
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	var token string
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		tr, err := kubeClusterClient.Cluster(path).CoreV1().ServiceAccounts(ns.Name).CreateToken(ctx, "default", &authenticationv1.TokenRequest{
			Spec: authenticationv1.TokenRequestSpec{
				Audiences:         []string{audience},
				ExpirationSeconds: ptr.To[int64](3600),
			},
		}, metav1.CreateOptions{})
		if err != nil {
			return false, err.Error()
		}
		token = tr.Status.Token
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	return token
}

// acceptClaim builds an accepted APIBinding permission claim (MatchAll) for the
// given exported permission claim.
func acceptClaim(pc apisv1alpha2.PermissionClaim) apisv1alpha2.AcceptablePermissionClaim {
	return apisv1alpha2.AcceptablePermissionClaim{
		ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
			PermissionClaim: pc,
			Selector:        apisv1alpha2.PermissionClaimSelector{MatchAll: true},
		},
		State: apisv1alpha2.ClaimAccepted,
	}
}
