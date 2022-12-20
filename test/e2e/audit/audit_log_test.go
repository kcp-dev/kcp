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

package audit

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/apis/audit"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAuditLogs(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.PrivateKcpServer(t, framework.WithCustomArguments([]string{"--audit-log-path", "./audit-log", "--audit-policy-file", "./policy.yaml"}...))

	ctx, cancelFunc := context.WithCancel(context.Background())

	t.Cleanup(func() {
		os.Remove("./audit-log")
		cancelFunc()
	})

	cfg := server.BaseConfig(t)

	clusterName := framework.NewOrganizationFixture(t, server)
	workspaceKubeClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	_, err = workspaceKubeClient.Cluster(clusterName.Path()).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "Error listing configmaps")

	data, err := os.ReadFile("./audit-log")
	require.NoError(t, err, "Error reading auditfile")

	lines := strings.Split(string(data), "\n")
	var auditEvent audit.Event
	err = json.Unmarshal([]byte(lines[0]), &auditEvent)
	require.NoError(t, err, "Error parsing JSON data")

	workspaceNameSent := clusterName.String()
	workspaceNameRecvd := auditEvent.Annotations["tenancy.kcp.dev/workspace"]

	require.Equal(t, workspaceNameSent, workspaceNameRecvd)

}
