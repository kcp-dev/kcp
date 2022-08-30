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

	kcpclienthelper "github.com/kcp-dev/apimachinery/pkg/client"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/apis/audit"
	kubernetesclientset "k8s.io/client-go/kubernetes"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAuditLogs(t *testing.T) {
	server := framework.PrivateKcpServer(t, []string{"--audit-log-path", "./audit-log", "--audit-policy-file", "./policy.yaml"}...)

	ctx, cancelFunc := context.WithCancel(context.Background())

	t.Cleanup(func() {
		os.Remove("./audit-log")
		cancelFunc()
	})

	cfg := server.BaseConfig(t)

	workspaceName := framework.NewOrganizationFixture(t, server)
	workspaceCfg := kcpclienthelper.SetCluster(cfg, workspaceName)
	workspaceKubeClient, err := kubernetesclientset.NewForConfig(workspaceCfg)
	require.NoError(t, err)

	_, err = workspaceKubeClient.CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "Error listing configmaps")

	data, err := os.ReadFile("./audit-log")
	require.NoError(t, err, "Error reading auditfile")

	lines := strings.Split(string(data), "\n")
	var auditEvent audit.Event
	err = json.Unmarshal([]byte(lines[0]), &auditEvent)
	require.NoError(t, err, "Error parsing JSON data")

	workspaceNameSent := workspaceName.String()
	workspaceNameRecvd := auditEvent.Annotations["tenancy.kcp.dev/workspace"]

	require.Equal(t, workspaceNameSent, workspaceNameRecvd)

}
