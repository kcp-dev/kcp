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

package cluster

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestSyncerHeartbeat(t *testing.T) {
	t.Parallel()

	source := framework.SharedKcpServer(t)

	t.Log("Creating an organization")
	orgClusterName := framework.NewOrganizationFixture(t, source)

	t.Log("Creating a workspace")
	wsClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName, "Universal")

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	syncerFixture := framework.NewSyncerFixture(t, sets.NewString(), source, orgClusterName, wsClusterName)
	syncerFixture.WaitForClusterReadyReason(t, workloadv1alpha1.ErrorHeartbeatMissedReason)
	syncerFixture.Start(t, ctx)
	syncerFixture.WaitForClusterReadyReason(t, "")
}
