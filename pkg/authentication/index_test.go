/*
Copyright 2025 The kcp Authors.

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

package authentication

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/index"
)

func TestCrossShardWorkspaceType(t *testing.T) {
	t.Parallel()

	const (
		shardName   = "shard-1"
		teamCluster = "logicalteamcluster"
	)

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(errors.New("test has ended"))

	clusterIndex := index.New(nil)
	authIndex := NewIndex(ctx, nil)

	// setup
	clusterIndex.UpsertShard("root", "https://root.io")
	clusterIndex.UpsertShard(shardName, "https://buck.io")

	// setup the root logicalcluster (has no workspace)
	clusterIndex.UpsertLogicalCluster("root", newLogicalCluster("root", "root:root"))

	// place the custom workspace type
	authIndex.UpsertWorkspaceType("root", newWorkspaceType("custom-type", "root"))

	// setup the team workspace (ws is on root shard in root workspace, cluster is on the 2nd shard)
	ws := newWorkspace("team1", "root", teamCluster)
	ws.Spec.Type = &tenancyv1alpha1.WorkspaceTypeReference{
		Name: "custom-type",
		Path: "root",
	}
	clusterIndex.UpsertWorkspace("root", ws)
	clusterIndex.UpsertLogicalCluster(shardName, newLogicalCluster(teamCluster, "root:custom-type"))

	r, found := clusterIndex.Lookup(logicalcluster.NewPath("root:team1"))
	require.True(t, found)
	require.Equal(t, shardName, r.Shard)
	require.Equal(t, teamCluster, r.Cluster.String())
	require.Equal(t, "root:custom-type", r.Type.String())
}

func newWorkspaceType(name, cluster string) *tenancyv1alpha1.WorkspaceType {
	return &tenancyv1alpha1.WorkspaceType{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{"kcp.io/cluster": cluster}},
		Spec:       tenancyv1alpha1.WorkspaceTypeSpec{},
	}
}

func newWorkspace(name, cluster, scheduledCluster string) *tenancyv1alpha1.Workspace {
	return &tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{"kcp.io/cluster": cluster}},
		Spec:       tenancyv1alpha1.WorkspaceSpec{Cluster: scheduledCluster},
		Status:     tenancyv1alpha1.WorkspaceStatus{Phase: corev1alpha1.LogicalClusterPhaseReady},
	}
}

func WithCondition(ws *tenancyv1alpha1.Workspace, condition conditionsv1alpha1.Condition) *tenancyv1alpha1.Workspace {
	ws.Status.Conditions = []conditionsv1alpha1.Condition{condition}
	return ws
}

func newLogicalCluster(cluster string, fqType string) *corev1alpha1.LogicalCluster {
	return &corev1alpha1.LogicalCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
			Annotations: map[string]string{
				"kcp.io/cluster": cluster,
				tenancyv1alpha1.LogicalClusterTypeAnnotationKey: fqType,
			},
		},
	}
}
