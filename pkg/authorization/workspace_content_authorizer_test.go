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

package authorization

import (
	"context"
	"testing"

	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	kcpfakeclient "github.com/kcp-dev/client-go/kubernetes/fake"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	v1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	authserviceaccount "k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
	"k8s.io/kubernetes/pkg/controller"
	"k8s.io/kubernetes/pkg/genericcontrolplane"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
)

func newUser(name string, groups ...string) *user.DefaultInfo {
	return &user.DefaultInfo{
		Name:   name,
		Groups: groups,
	}
}

func newServiceAccountWithCluster(name string, cluster string, groups ...string) *user.DefaultInfo {
	extra := make(map[string][]string)
	if len(cluster) > 0 {
		extra[authserviceaccount.ClusterNameKey] = []string{cluster}
	}
	return &user.DefaultInfo{
		Name:   name,
		Extra:  extra,
		Groups: groups,
	}
}

type recordingAuthorizer struct {
	err      error
	decision authorizer.Decision
	reason   string

	recordedAttributes authorizer.Attributes
}

func (r *recordingAuthorizer) Authorize(ctx context.Context, a authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	r.recordedAttributes = a
	return r.decision, r.reason, r.err
}

func TestWorkspaceContentAuthorizer(t *testing.T) {
	for _, tt := range []struct {
		testName              string
		requestedWorkspace    string
		requestingUser        *user.DefaultInfo
		wantReason, wantError string
		wantDecision          authorizer.Decision
		deepSARHeader         bool
	}{
		{
			testName: "unknown requested workspace",

			requestedWorkspace: "root:unknown",
			requestingUser:     newUser("user-access"),
			wantDecision:       authorizer.DecisionDeny,
			wantReason:         "thisworkspace not found",
		},
		{
			testName: "workspace without parent",

			requestedWorkspace: "rootwithoutparent",
			requestingUser:     newUser("user-access"),
			wantDecision:       authorizer.DecisionAllow,
		},
		{
			testName: "non-permitted user is not allowed",

			requestedWorkspace: "root:ready",
			requestingUser:     newUser("user-unknown"),
			wantDecision:       authorizer.DecisionNoOpinion,
			wantReason:         "no verb=access permission on /",
		},
		{
			testName: "permitted user is granted access",

			requestedWorkspace: "root:ready",
			requestingUser:     newUser("user-access", "system:authenticated"),
			wantDecision:       authorizer.DecisionAllow,
		},
		{
			testName: "service account from other cluster is denied",

			requestedWorkspace: "root:ready",
			requestingUser:     newServiceAccountWithCluster("sa", "anotherws"),
			wantDecision:       authorizer.DecisionDeny,
			wantReason:         "service account from different workspace",
		},
		{
			testName: "service account from same cluster is granted access",

			requestedWorkspace: "root:ready",
			requestingUser:     newServiceAccountWithCluster("sa", "root:ready"),
			wantDecision:       authorizer.DecisionAllow,
		},
		{
			testName: "user is granted access on root",

			requestedWorkspace: "root",
			requestingUser:     newUser("somebody", "system:authenticated"),
			wantDecision:       authorizer.DecisionAllow,
		},
		{
			testName: "service account from other cluster is denied on root",

			requestedWorkspace: "root",
			requestingUser:     newServiceAccountWithCluster("somebody", "someworkspace", "system:authenticated"),
			wantDecision:       authorizer.DecisionDeny,
			wantReason:         "service account from different workspace",
		},
		{
			testName: "service account from root cluster is granted access on root",

			requestedWorkspace: "root",
			requestingUser:     newServiceAccountWithCluster("somebody", "root", "system:authenticated"),
			wantDecision:       authorizer.DecisionAllow,
		},
		{
			testName: "service account of same workspace is not allowed access to scheduling workspace",

			requestedWorkspace: "root:scheduling",
			requestingUser:     newServiceAccountWithCluster("somebody", "root:scheduling", "system:authenticated"),
			wantDecision:       authorizer.DecisionNoOpinion,
			wantReason:         "not permitted due to phase \"Scheduling\"",
		},
		{
			testName: "service account of same workspace is denied on initializing workspace",

			requestedWorkspace: "root:initializing",
			requestingUser:     newServiceAccountWithCluster("somebody", "root:initializing", "system:authenticated"),
			wantDecision:       authorizer.DecisionAllow,
		},
		{
			testName: "system:kcp:logical-cluster-admin can always pass",

			requestedWorkspace: "root:non-existent",
			requestingUser:     newUser("lcluster-admin", "system:kcp:logical-cluster-admin"),
			wantDecision:       authorizer.DecisionAllow,
		},
		{
			testName: "permitted user is granted access to initializing workspace",

			requestedWorkspace: "root:initializing",
			requestingUser:     newUser("user-access", "system:authenticated"),
			wantDecision:       authorizer.DecisionAllow,
		},
		{
			testName: "any user passed for deep SAR",

			requestedWorkspace: "root:ready",
			requestingUser:     newUser("user-unknown"),
			deepSARHeader:      true,
			wantDecision:       authorizer.DecisionAllow,
		},
		{
			testName: "any service account passed for deep SAR",

			requestedWorkspace: "root:ready",
			requestingUser:     newServiceAccountWithCluster("somebody", "root", "system:authenticated"),
			deepSARHeader:      true,
			wantDecision:       authorizer.DecisionAllow,
		},
	} {
		t.Run(tt.testName, func(t *testing.T) {
			ctx := context.Background()

			kubeClient := kcpfakeclient.NewSimpleClientset(
				&v1.ClusterRole{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: genericcontrolplane.LocalAdminCluster.String(),
						},
						Name: "access",
					},
					Rules: []v1.PolicyRule{
						{
							Verbs:           []string{"access"},
							NonResourceURLs: []string{"/"},
						},
					},
				},
				&v1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root",
						},
						Name: "system:authenticated:access",
					},
					Subjects: []v1.Subject{
						{
							Kind:     "Group",
							APIGroup: "rbac.authorization.k8s.io",
							Name:     "system:authenticated",
						},
					},
					RoleRef: v1.RoleRef{
						APIGroup: "rbac.authorization.k8s.io",
						Kind:     "ClusterRole",
						Name:     "access",
					},
				},
				&v1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root:ready",
						},
						Name: "user-access-ready-access",
					},
					Subjects: []v1.Subject{
						{
							Kind:     "User",
							APIGroup: "rbac.authorization.k8s.io",
							Name:     "user-access",
						},
					},
					RoleRef: v1.RoleRef{
						APIGroup: "rbac.authorization.k8s.io",
						Kind:     "ClusterRole",
						Name:     "access",
					},
				},
				&v1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root:initializing",
						},
						Name: "user-access-initializing-access",
					},
					Subjects: []v1.Subject{
						{
							Kind:     "User",
							APIGroup: "rbac.authorization.k8s.io",
							Name:     "user-access",
						},
					},
					RoleRef: v1.RoleRef{
						APIGroup: "rbac.authorization.k8s.io",
						Kind:     "ClusterRole",
						Name:     "access",
					},
				},
				&v1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "rootwithoutparent",
						},
						Name: "system:authenticated:access",
					},
					Subjects: []v1.Subject{
						{
							Kind:     "User",
							APIGroup: "rbac.authorization.k8s.io",
							Name:     "user-access",
						},
					},
					RoleRef: v1.RoleRef{
						APIGroup: "rbac.authorization.k8s.io",
						Kind:     "ClusterRole",
						Name:     "access",
					},
				},
			)
			kubeShareInformerFactory := kcpkubernetesinformers.NewSharedInformerFactory(kubeClient, controller.NoResyncPeriodFunc())
			informers := []cache.SharedIndexInformer{
				kubeShareInformerFactory.Rbac().V1().Roles().Informer(),
				kubeShareInformerFactory.Rbac().V1().RoleBindings().Informer(),
				kubeShareInformerFactory.Rbac().V1().ClusterRoles().Informer(),
				kubeShareInformerFactory.Rbac().V1().ClusterRoleBindings().Informer(),
			}
			var syncs []cache.InformerSynced
			for i := range informers {
				go informers[i].Run(ctx.Done())
				syncs = append(syncs, informers[i].HasSynced)
			}
			cache.WaitForCacheSync(ctx.Done(), syncs...)

			indexer := cache.NewIndexer(kcpcache.MetaClusterNamespaceKeyFunc, cache.Indexers{})
			require.NoError(t, indexer.Add(&tenancyv1alpha1.ThisWorkspace{
				ObjectMeta: metav1.ObjectMeta{Name: tenancyv1alpha1.ThisWorkspaceName, Annotations: map[string]string{logicalcluster.AnnotationKey: "root"}},
				Status:     tenancyv1alpha1.ThisWorkspaceStatus{Phase: tenancyv1alpha1.WorkspacePhaseReady},
			}))
			require.NoError(t, indexer.Add(&tenancyv1alpha1.ThisWorkspace{
				ObjectMeta: metav1.ObjectMeta{Name: tenancyv1alpha1.ThisWorkspaceName, Annotations: map[string]string{logicalcluster.AnnotationKey: "root:ready"}},
				Status:     tenancyv1alpha1.ThisWorkspaceStatus{Phase: tenancyv1alpha1.WorkspacePhaseReady},
			}))
			require.NoError(t, indexer.Add(&tenancyv1alpha1.ThisWorkspace{
				ObjectMeta: metav1.ObjectMeta{Name: tenancyv1alpha1.ThisWorkspaceName, Annotations: map[string]string{logicalcluster.AnnotationKey: "root:scheduling"}},
				Status:     tenancyv1alpha1.ThisWorkspaceStatus{Phase: tenancyv1alpha1.WorkspacePhaseScheduling},
			}))
			require.NoError(t, indexer.Add(&tenancyv1alpha1.ThisWorkspace{
				ObjectMeta: metav1.ObjectMeta{Name: tenancyv1alpha1.ThisWorkspaceName, Annotations: map[string]string{logicalcluster.AnnotationKey: "root:initializing"}},
				Status:     tenancyv1alpha1.ThisWorkspaceStatus{Phase: tenancyv1alpha1.WorkspacePhaseInitializing},
			}))
			require.NoError(t, indexer.Add(&tenancyv1alpha1.ThisWorkspace{
				ObjectMeta: metav1.ObjectMeta{Name: tenancyv1alpha1.ThisWorkspaceName, Annotations: map[string]string{logicalcluster.AnnotationKey: "rootwithoutparent"}},
				Status:     tenancyv1alpha1.ThisWorkspaceStatus{Phase: tenancyv1alpha1.WorkspacePhaseReady},
			}))
			lister := tenancyv1alpha1listers.NewThisWorkspaceClusterLister(indexer)

			recordingAuthorizer := &recordingAuthorizer{decision: authorizer.DecisionAllow}
			w := NewWorkspaceContentAuthorizer(kubeShareInformerFactory, lister, recordingAuthorizer)

			requestedCluster := request.Cluster{
				Name: logicalcluster.New(tt.requestedWorkspace),
			}
			ctx = request.WithCluster(ctx, requestedCluster)
			attr := authorizer.AttributesRecord{
				User: tt.requestingUser,
			}
			if tt.deepSARHeader {
				ctx = context.WithValue(ctx, deepSARKey, true)
			}

			gotDecision, gotReason, err := w.Authorize(ctx, attr)
			gotErr := ""
			if err != nil {
				gotErr = err.Error()
			}

			if gotErr != tt.wantError {
				t.Errorf("want error %q, got %q", tt.wantError, gotErr)
			}

			if gotReason != tt.wantReason {
				t.Errorf("want reason %q, got %q", tt.wantReason, gotReason)
			}

			if gotDecision != tt.wantDecision {
				t.Errorf("want decision %v, got %v", tt.wantDecision, gotDecision)
			}
		})
	}
}
