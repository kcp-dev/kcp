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

	"github.com/kcp-dev/logicalcluster/v3"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
)

func TestRequiredGroupsAuthorizer(t *testing.T) {

	for name, tt := range map[string]struct {
		requestedWorkspace    string
		requestingUser        *user.DefaultInfo
		wantReason, wantError string
		wantDecision          authorizer.Decision
		deepSARHeader         bool
		logicalCluster        *v1alpha1.LogicalCluster
	}{
		"deep SAR": {
			requestedWorkspace: "root:ready",
			requestingUser:     newUser("user-unknown"),
			deepSARHeader:      true,
			wantDecision:       authorizer.DecisionAllow,
			wantReason:         "delegating due to deep SAR request: allowed",
		},
		"missing cluster in request": {
			requestingUser: newUser("user-unknown"),
			wantDecision:   authorizer.DecisionNoOpinion,
			wantReason:     "empty cluster name",
		},
		"system:kcp:logical-cluster-admin can always pass": {
			requestedWorkspace: "root:ready",
			requestingUser:     newUser("lcluster-admin", "system:kcp:logical-cluster-admin"),
			wantDecision:       authorizer.DecisionAllow,
			wantReason:         "delegating due to logical cluster admin access: allowed",
		},
		"service account from other cluster is granted access": {
			requestedWorkspace: "root:ready",
			requestingUser:     newServiceAccountWithCluster("sa", "anotherws"),
			wantDecision:       authorizer.DecisionAllow,
			wantReason:         "delegating due to service account access to logical cluster: allowed",
		},
		"service account from same cluster is granted access": {
			requestedWorkspace: "root:ready",
			requestingUser:     newServiceAccountWithCluster("sa", "root:ready"),
			wantDecision:       authorizer.DecisionAllow,
			wantReason:         "delegating due to service account access to logical cluster: allowed",
		},
		"permitted user is granted access to logical cluster without required groups": {
			requestedWorkspace: "root:ready",
			requestingUser:     newUser("user-access", "system:authenticated"),
			wantDecision:       authorizer.DecisionAllow,
			logicalCluster:     &v1alpha1.LogicalCluster{},
			wantReason:         "delegating due to logical cluster does not require groups: allowed",
		},
		"permitted user is denied access to logical cluster with required groups": {
			requestedWorkspace: "root:ready",
			requestingUser:     newUser("user-access", "system:authenticated"),
			wantDecision:       authorizer.DecisionDeny,
			logicalCluster: &v1alpha1.LogicalCluster{
				ObjectMeta: v1.ObjectMeta{
					Annotations: map[string]string{
						"authorization.kcp.dev/required-groups": "special-group",
					},
				},
			},
			wantReason: "user is not a member of required groups: special-group",
		},
		"permitted user is allowed access to logical cluster with matching all of multiple disjunctive groups": {
			requestedWorkspace: "root:ready",
			requestingUser:     newUser("user-access", "special-group-1", "special-group-2"),
			wantDecision:       authorizer.DecisionAllow,
			logicalCluster: &v1alpha1.LogicalCluster{
				ObjectMeta: v1.ObjectMeta{
					Annotations: map[string]string{
						"authorization.kcp.dev/required-groups": "special-group-1;special-group-2",
					},
				},
			},
			wantReason: "delegating due to user is member of required groups: special-group-1;special-group-2: allowed",
		},
		"permitted user is allowed access to logical cluster with matching one of multiple disjunctive groups": {
			requestedWorkspace: "root:ready",
			requestingUser:     newUser("user-access", "special-group-1", "other-group"),
			wantDecision:       authorizer.DecisionAllow,
			logicalCluster: &v1alpha1.LogicalCluster{
				ObjectMeta: v1.ObjectMeta{
					Annotations: map[string]string{
						"authorization.kcp.dev/required-groups": "special-group-1;special-group-2",
					},
				},
			},
			wantReason: "delegating due to user is member of required groups: special-group-1;special-group-2: allowed",
		},
		"permitted user is allowed access to logical cluster with multiple conjunctive groups": {
			requestedWorkspace: "root:ready",
			requestingUser:     newUser("user-access", "special-group-1", "special-group-2"),
			wantDecision:       authorizer.DecisionAllow,
			logicalCluster: &v1alpha1.LogicalCluster{
				ObjectMeta: v1.ObjectMeta{
					Annotations: map[string]string{
						"authorization.kcp.dev/required-groups": "special-group-1,special-group-2",
					},
				},
			},
			wantReason: "delegating due to user is member of required groups: special-group-1,special-group-2: allowed",
		},
		"permitted user is denied access to logical cluster with multiple conjunctive groups": {
			requestedWorkspace: "root:ready",
			requestingUser:     newUser("user-access", "special-group-1", "other-group"),
			wantDecision:       authorizer.DecisionDeny,
			logicalCluster: &v1alpha1.LogicalCluster{
				ObjectMeta: v1.ObjectMeta{
					Annotations: map[string]string{
						"authorization.kcp.dev/required-groups": "special-group-1,special-group-2",
					},
				},
			},
			wantReason: "user is not a member of required groups: special-group-1,special-group-2",
		},
		"permitted user is allowed access to logical cluster with matching two of multiple conjunctive and disjunctive groups": {
			requestedWorkspace: "root:ready",
			requestingUser:     newUser("user-access", "special-group-1", "special-group-2"),
			wantDecision:       authorizer.DecisionAllow,
			logicalCluster: &v1alpha1.LogicalCluster{
				ObjectMeta: v1.ObjectMeta{
					Annotations: map[string]string{
						"authorization.kcp.dev/required-groups": "special-group-1,special-group-2;special-group-3",
					},
				},
			},
			wantReason: "delegating due to user is member of required groups: special-group-1,special-group-2;special-group-3: allowed",
		},
		"permitted user is allowed access to logical cluster with matching one of multiple conjunctive and disjunctive groups": {
			requestedWorkspace: "root:ready",
			requestingUser:     newUser("user-access", "special-group-3"),
			wantDecision:       authorizer.DecisionAllow,
			logicalCluster: &v1alpha1.LogicalCluster{
				ObjectMeta: v1.ObjectMeta{
					Annotations: map[string]string{
						"authorization.kcp.dev/required-groups": "special-group-1,special-group-2;special-group-3",
					},
				},
			},
			wantReason: "delegating due to user is member of required groups: special-group-1,special-group-2;special-group-3: allowed",
		},
		"permitted user is denied access to logical cluster with matching only one of multiple conjunctive and disjunctive groups": {
			requestedWorkspace: "root:ready",
			requestingUser:     newUser("user-access", "special-group-1"),
			wantDecision:       authorizer.DecisionDeny,
			logicalCluster: &v1alpha1.LogicalCluster{
				ObjectMeta: v1.ObjectMeta{
					Annotations: map[string]string{
						"authorization.kcp.dev/required-groups": "special-group-1,special-group-2;special-group-3",
					},
				},
			},
			wantReason: "user is not a member of required groups: special-group-1,special-group-2;special-group-3",
		},
		"permitted user is denied access to logical cluster with matching none of multiple conjunctive and disjunctive groups": {
			requestedWorkspace: "root:ready",
			requestingUser:     newUser("user-access", "system:authenticated"),
			wantDecision:       authorizer.DecisionDeny,
			logicalCluster: &v1alpha1.LogicalCluster{
				ObjectMeta: v1.ObjectMeta{
					Annotations: map[string]string{
						"authorization.kcp.dev/required-groups": "special-group-1,special-group-2;special-group-3",
					},
				},
			},
			wantReason: "user is not a member of required groups: special-group-1,special-group-2;special-group-3",
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if tt.requestedWorkspace != "" {
				ctx = request.WithCluster(ctx, request.Cluster{
					Name: logicalcluster.Name(tt.requestedWorkspace),
				})
			}
			if tt.deepSARHeader {
				ctx = context.WithValue(ctx, deepSARKey, true)
			}

			recordingAuthorizer := &recordingAuthorizer{decision: authorizer.DecisionAllow, reason: "allowed"}
			attr := authorizer.AttributesRecord{
				User: tt.requestingUser,
			}
			authz := requiredGroupsAuthorizer{
				getLogicalCluster: func(logicalCluster logicalcluster.Name) (*v1alpha1.LogicalCluster, error) {
					return tt.logicalCluster, nil
				},
				delegate: recordingAuthorizer,
			}

			gotDecision, gotReason, err := authz.Authorize(ctx, attr)
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
