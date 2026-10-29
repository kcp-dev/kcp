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
	"context"
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
		initializers []corev1alpha1.LogicalClusterInitializer
		terminators  []corev1alpha1.LogicalClusterTerminator
		groups       []string
		deepSAR      bool
		wantDecision authorizer.Decision
		wantReason   string
	}{
		"registered initializer in initializing phase is allowed": {
			requested:    logicalcluster.Name("root:ws"),
			phase:        corev1alpha1.LogicalClusterPhaseInitializing,
			initializers: []corev1alpha1.LogicalClusterInitializer{initializer},
			groups:       []string{InitializerGroup(initializer)},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "initializing lifecycle VW pre-authorized access",
		},
		"registered terminator in terminating phase is allowed": {
			requested:    logicalcluster.Name("root:ws"),
			phase:        corev1alpha1.LogicalClusterPhaseTerminating,
			terminators:  []corev1alpha1.LogicalClusterTerminator{terminator},
			groups:       []string{TerminatorGroup(terminator)},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "terminating lifecycle VW pre-authorized access",
		},
		"registered terminator in deleting phase is allowed": {
			requested:    logicalcluster.Name("root:ws"),
			phase:        corev1alpha1.LogicalClusterPhaseDeleting,
			terminators:  []corev1alpha1.LogicalClusterTerminator{terminator},
			groups:       []string{TerminatorGroup(terminator)},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "terminating lifecycle VW pre-authorized access",
		},
		"unregistered terminator in deleting phase is not allowed": {
			requested:    logicalcluster.Name("root:ws"),
			phase:        corev1alpha1.LogicalClusterPhaseDeleting,
			terminators:  []corev1alpha1.LogicalClusterTerminator{terminator},
			groups:       []string{TerminatorGroup("probe")},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   `no registered lifecycle group in phase "Deleting"`,
		},
		"registered initializer in ready phase is not allowed": {
			requested:    logicalcluster.Name("root:ws"),
			phase:        corev1alpha1.LogicalClusterPhaseReady,
			initializers: []corev1alpha1.LogicalClusterInitializer{initializer},
			groups:       []string{InitializerGroup(initializer)},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   `no registered lifecycle group in phase "Ready"`,
		},
		"initializer group in terminating phase is not allowed": {
			requested:    logicalcluster.Name("root:ws"),
			phase:        corev1alpha1.LogicalClusterPhaseTerminating,
			initializers: []corev1alpha1.LogicalClusterInitializer{initializer},
			terminators:  []corev1alpha1.LogicalClusterTerminator{terminator},
			groups:       []string{InitializerGroup(initializer)},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   `no registered lifecycle group in phase "Terminating"`,
		},
		"name registered as terminator is not accepted as initializer group": {
			requested:    logicalcluster.Name("root:ws"),
			phase:        corev1alpha1.LogicalClusterPhaseInitializing,
			terminators:  []corev1alpha1.LogicalClusterTerminator{terminator},
			groups:       []string{InitializerGroup(corev1alpha1.LogicalClusterInitializer(terminator))},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   `no registered lifecycle group in phase "Initializing"`,
		},
		"no lifecycle group": {
			requested:    logicalcluster.Name("root:ws"),
			phase:        corev1alpha1.LogicalClusterPhaseInitializing,
			initializers: []corev1alpha1.LogicalClusterInitializer{initializer},
			groups:       []string{"system:authenticated"},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "no lifecycle group",
		},
		"lifecycle group without logical cluster is not allowed": {
			requested:    logicalcluster.Name("root:gone"),
			phase:        corev1alpha1.LogicalClusterPhaseInitializing,
			initializers: []corev1alpha1.LogicalClusterInitializer{initializer},
			groups:       []string{InitializerGroup(initializer)},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "LogicalCluster not found",
		},
		"empty cluster is skipped": {
			requested:    logicalcluster.Name(""),
			phase:        corev1alpha1.LogicalClusterPhaseInitializing,
			initializers: []corev1alpha1.LogicalClusterInitializer{initializer},
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
			if cas.deepSAR {
				ctx = context.WithValue(ctx, deepSARKey, true)
			}

			logicalCluster := newLogicalCluster(logicalcluster.Name("root:ws"), cas.phase)
			logicalCluster.Status.Initializers = cas.initializers
			logicalCluster.Status.Terminators = cas.terminators

			a := NewLifecycleGroupsAuthorizer(stubLogicalClusterGetter(logicalCluster))

			attr := authorizer.AttributesRecord{User: &user.DefaultInfo{Name: "lifecycle-caller", Groups: cas.groups}}

			decision, reason, err := a.Authorize(ctx, attr)
			require.NoError(t, err)
			require.Equal(t, cas.wantDecision, decision)
			require.Equal(t, cas.wantReason, reason)
		})
	}
}
