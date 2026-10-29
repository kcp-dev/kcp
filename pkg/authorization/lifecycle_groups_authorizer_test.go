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

package authorization

import (
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
)

func TestLifecycleGroupsAuthorizer(t *testing.T) {
	t.Parallel()

	const (
		initializer corev1alpha1.LogicalClusterInitializer = "test-initializer"
		terminator  corev1alpha1.LogicalClusterTerminator  = "test-terminator"
	)

	cases := map[string]struct {
		requested    logicalcluster.Name
		phase        corev1alpha1.LogicalClusterPhaseType
		groups       []string
		wantDecision authorizer.Decision
		wantReason   string
	}{
		"initializer group in initializing phase is allowed": {
			requested:    logicalcluster.Name("root:ws"),
			phase:        corev1alpha1.LogicalClusterPhaseInitializing,
			groups:       []string{InitializerGroup(initializer)},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "initializing lifecycle VW pre-authorized access",
		},
		"terminator group in terminating phase is allowed": {
			requested:    logicalcluster.Name("root:ws"),
			phase:        corev1alpha1.LogicalClusterPhaseTerminating,
			groups:       []string{TerminatorGroup(terminator)},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "terminating lifecycle VW pre-authorized access",
		},
		"terminator group in deleting phase is allowed": {
			requested:    logicalcluster.Name("root:ws"),
			phase:        corev1alpha1.LogicalClusterPhaseDeleting,
			groups:       []string{TerminatorGroup(terminator)},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "terminating lifecycle VW pre-authorized access",
		},
		// Locks in that a forged or leftover lifecycle group on a caller of a workspace that
		// is not in a lifecycle phase does not grant access.
		"initializer group in ready phase is not allowed": {
			requested:    logicalcluster.Name("root:ws"),
			phase:        corev1alpha1.LogicalClusterPhaseReady,
			groups:       []string{InitializerGroup(initializer)},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   `lifecycle group in phase "Ready"`,
		},
		"no lifecycle group is not allowed": {
			requested:    logicalcluster.Name("root:ws"),
			phase:        corev1alpha1.LogicalClusterPhaseInitializing,
			groups:       []string{"system:authenticated"},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "no lifecycle group",
		},
		"lifecycle group without logical cluster is not allowed": {
			requested:    logicalcluster.Name("root:gone"),
			phase:        corev1alpha1.LogicalClusterPhaseInitializing,
			groups:       []string{InitializerGroup(initializer)},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "LogicalCluster not found",
		},
		"empty cluster is skipped": {
			requested:    logicalcluster.Name(""),
			phase:        corev1alpha1.LogicalClusterPhaseInitializing,
			groups:       []string{InitializerGroup(initializer)},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "empty cluster name",
		},
	}

	for title, cas := range cases {
		t.Run(title, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			ctx = genericapirequest.WithCluster(ctx, genericapirequest.Cluster{Name: cas.requested})

			getter := stubLogicalClusterGetter(newLogicalCluster(logicalcluster.Name("root:ws"), cas.phase))
			a := NewLifecycleGroupsAuthorizer(getter)

			attr := authorizer.AttributesRecord{User: &user.DefaultInfo{Name: "lifecycle-caller", Groups: cas.groups}}

			decision, reason, err := a.Authorize(ctx, attr)
			require.NoError(t, err)
			require.Equal(t, cas.wantDecision, decision)
			require.Equal(t, cas.wantReason, reason)
		})
	}
}
