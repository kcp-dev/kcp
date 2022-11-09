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
	"reflect"
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

func newServiceAccount(name string, groups ...string) *user.DefaultInfo {
	return &user.DefaultInfo{
		Name:   name,
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
		wantUser              *user.DefaultInfo
		deepSARHeader         bool
	}{
		{
			testName: "requested cluster is not root",

			requestedWorkspace: "unknown",
			requestingUser:     newUser("user-1"),
			wantDecision:       authorizer.DecisionNoOpinion,
			wantReason:         "empty or non root workspace",
		},
		{
			testName: "unknown requested workspace",

			requestedWorkspace: "root:unknown",
			requestingUser:     newUser("user-1"),
			wantDecision:       authorizer.DecisionDeny,
			wantReason:         "clusterworkspace not found",
		},
		{
			testName: "workspace without parent",

			requestedWorkspace: "rootwithoutparent",
			requestingUser:     newUser("user-1"),
			wantDecision:       authorizer.DecisionNoOpinion,
			wantReason:         "non-root workspace that does not have a parent",
		},
		{
			testName: "non-permitted user is denied",

			requestedWorkspace: "root:ready",
			requestingUser:     newUser("user-unknown"),
			wantDecision:       authorizer.DecisionNoOpinion,
			wantReason:         "not permitted, subject has not been granted any groups",
		},
		{
			testName: "permitted admin user is granted admin",

			requestedWorkspace: "root:ready",
			requestingUser:     newUser("user-admin"),
			wantUser:           newUser("user-admin", "system:kcp:clusterworkspace:access", "system:kcp:clusterworkspace:admin"),
			wantDecision:       authorizer.DecisionAllow,
		},
		{
			testName: "permitted access user is granted access",

			requestedWorkspace: "root:ready",
			requestingUser:     newUser("user-access"),
			wantUser:           newUser("user-access", "system:kcp:clusterworkspace:access"),
			wantDecision:       authorizer.DecisionAllow,
		},
		{
			testName: "non-permitted service account is denied",

			requestedWorkspace: "root:ready",
			requestingUser:     newServiceAccountWithCluster("sa", "anotherws"),
			wantDecision:       authorizer.DecisionNoOpinion,
			wantReason:         "not permitted, subject has not been granted any groups",
		},
		{
			testName: "permitted service account is granted access",

			requestedWorkspace: "root:ready",
			requestingUser:     newServiceAccountWithCluster("sa", "root:ready"),
			wantUser:           newServiceAccountWithCluster("sa", "root:ready", "system:kcp:clusterworkspace:access"),
			wantDecision:       authorizer.DecisionAllow,
		},
		{
			testName: "authenticated user is granted access on root",

			requestedWorkspace: "root",
			requestingUser:     newUser("somebody", "system:authenticated"),
			wantUser:           newUser("somebody", "system:authenticated", "system:kcp:clusterworkspace:access"),
			wantDecision:       authorizer.DecisionAllow,
		},
		{
			testName: "authenticated non-permitted service account is denied on root",

			requestedWorkspace: "root",
			requestingUser:     newServiceAccountWithCluster("somebody", "someworkspace", "system:authenticated"),
			wantDecision:       authorizer.DecisionNoOpinion,
			wantReason:         "root workspace access by non-root service account not permitted",
		},
		{
			testName: "authenticated permitted root service account is granted access on root",

			requestedWorkspace: "root",
			requestingUser:     newServiceAccountWithCluster("somebody", "root", "system:authenticated"),
			wantUser:           newServiceAccountWithCluster("somebody", "root", "system:authenticated", "system:kcp:clusterworkspace:access"),
			wantDecision:       authorizer.DecisionAllow,
		},
		{
			testName: "authenticated service account is denied on scheduling workspace",

			requestedWorkspace: "root:scheduling",
			requestingUser:     newServiceAccountWithCluster("somebody", "root", "system:authenticated"),
			wantDecision:       authorizer.DecisionNoOpinion,
			wantReason:         "not permitted due to phase \"Scheduling\"",
		},
		{
			testName: "permitted service account is denied on initializing workspace",

			requestedWorkspace: "root:initializing",
			requestingUser:     newServiceAccountWithCluster("somebody", "initializing", "system:authenticated"),
			wantDecision:       authorizer.DecisionNoOpinion,
			wantReason:         "not permitted, clusterworkspace is in initializing phase",
		},
		{
			testName: "system:kcp:logical-cluster-admin can always pass",

			requestedWorkspace: "root:non-existent",
			requestingUser:     newUser("lcluster-admin", "system:kcp:logical-cluster-admin"),
		},
		{
			testName: "permitted access user is denied on initializing workspace",

			requestedWorkspace: "root:initializing",
			requestingUser:     newUser("user-access"),
			wantDecision:       authorizer.DecisionNoOpinion,
			wantReason:         "not permitted, clusterworkspace is in initializing phase",
		},
		{
			testName: "permitted admin user is granted admin on initializing workspace",

			requestedWorkspace: "root:initializing",
			requestingUser:     newUser("user-admin"),
			wantUser:           newUser("user-admin", "system:kcp:clusterworkspace:access", "system:kcp:clusterworkspace:admin"),
			wantDecision:       authorizer.DecisionAllow,
		},
		{
			testName: "any user passed for deep SAR",

			requestedWorkspace: "root:ready",
			requestingUser:     newUser("user-unknown"),
			wantUser:           newUser("user-unknown"),
			deepSARHeader:      true,
			wantDecision:       authorizer.DecisionAllow,
		},
		{
			testName: "any service account passed for deep SAR as anyonmous",

			requestedWorkspace: "root:ready",
			requestingUser:     newServiceAccountWithCluster("somebody", "root", "system:authenticated"),
			wantUser:           newServiceAccount("system:anonymous", "system:authenticated"),
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
							logicalcluster.AnnotationKey: "root",
						},
						Name: "ready-admin",
					},
					Rules: []v1.PolicyRule{
						{
							Verbs:         []string{"admin"},
							Resources:     []string{"workspaces/content"},
							ResourceNames: []string{"ready"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
					},
				},
				&v1.ClusterRole{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root",
						},
						Name: "initializing-admin",
					},
					Rules: []v1.PolicyRule{
						{
							Verbs:         []string{"admin"},
							Resources:     []string{"workspaces/content"},
							ResourceNames: []string{"initializing"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
					},
				},
				&v1.ClusterRole{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root",
						},
						Name: "initializing-access",
					},
					Rules: []v1.PolicyRule{
						{
							Verbs:         []string{"access"},
							Resources:     []string{"workspaces/content"},
							ResourceNames: []string{"initializing"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
					},
				},
				&v1.ClusterRole{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root",
						},
						Name: "ready-access",
					},
					Rules: []v1.PolicyRule{
						{
							Verbs:         []string{"access"},
							Resources:     []string{"workspaces/content"},
							ResourceNames: []string{"ready"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
					},
				},
				&v1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root",
						},
						Name: "user-admin-ready-admin",
					},
					Subjects: []v1.Subject{
						{
							Kind:     "User",
							APIGroup: "rbac.authorization.k8s.io",
							Name:     "user-admin",
						},
					},
					RoleRef: v1.RoleRef{
						APIGroup: "rbac.authorization.k8s.io",
						Kind:     "ClusterRole",
						Name:     "ready-admin",
					},
				},
				&v1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root",
						},
						Name: "user-admin-initializing-admin",
					},
					Subjects: []v1.Subject{
						{
							Kind:     "User",
							APIGroup: "rbac.authorization.k8s.io",
							Name:     "user-admin",
						},
					},
					RoleRef: v1.RoleRef{
						APIGroup: "rbac.authorization.k8s.io",
						Kind:     "ClusterRole",
						Name:     "initializing-admin",
					},
				},
				&v1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root",
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
						Name:     "ready-access",
					},
				},
				&v1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "root",
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
						Name:     "initializing-access",
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
			require.NoError(t, indexer.Add(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{Name: "ready", Annotations: map[string]string{logicalcluster.AnnotationKey: "root"}},
				Status:     tenancyv1alpha1.ClusterWorkspaceStatus{Phase: tenancyv1alpha1.ClusterWorkspacePhaseReady},
			}))
			require.NoError(t, indexer.Add(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{Name: "scheduling", Annotations: map[string]string{logicalcluster.AnnotationKey: "root"}},
				Status:     tenancyv1alpha1.ClusterWorkspaceStatus{Phase: tenancyv1alpha1.ClusterWorkspacePhaseScheduling},
			}))
			require.NoError(t, indexer.Add(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{Name: "initializing", Annotations: map[string]string{logicalcluster.AnnotationKey: "root"}},
				Status:     tenancyv1alpha1.ClusterWorkspaceStatus{Phase: tenancyv1alpha1.ClusterWorkspacePhaseInitializing},
			}))
			lister := tenancyv1alpha1listers.NewClusterWorkspaceClusterLister(indexer)

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

			if tt.wantUser == nil {
				return
			}

			if recordingAuthorizer.recordedAttributes == nil {
				t.Errorf("want user %+v, got %+v", tt.wantUser, nil)
			} else if got := recordingAuthorizer.recordedAttributes.GetUser(); !reflect.DeepEqual(got, tt.wantUser) {
				t.Errorf("want user %+v, got %+v", tt.wantUser, got)
			}
		})
	}
}
