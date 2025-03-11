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
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/apis/audit"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"

	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestingserver "github.com/kcp-dev/kcp/sdk/testing/server"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAuditLogs(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	artifactDir, dataDir, err := kcptestingserver.ScratchDirs(t)
	require.NoError(t, err)

	server := kcptesting.PrivateKcpServer(t,
		kcptestingserver.WithCustomArguments(
			"--audit-policy-file", "./policy.yaml",
		),
		kcptestingserver.WithScratchDirectories(artifactDir, dataDir),
	)

	cfg := server.BaseConfig(t)

	t.Log("Creating org workspace")
	wsPath, ws := framework.NewOrganizationFixture(t, server)
	workspaceKubeClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Log("Listing configmaps")
	ctx := context.Background()
	_, err = workspaceKubeClient.Cluster(wsPath).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "Error listing configmaps")

	auditLogPath := filepath.Join(artifactDir, "kcp/main/kcp.audit")
	t.Logf("Reading audit log at %s", auditLogPath)
	data, err := os.ReadFile(auditLogPath)
	require.NoError(t, err, "Error reading auditfile")

	t.Log("Parsing audit log")
	lines := strings.Split(string(data), "\n")
	var requestAuditEvent, responseAuditEvent audit.Event
	err = json.Unmarshal([]byte(lines[0]), &requestAuditEvent)
	require.NoError(t, err, "Error parsing JSON data")
	err = json.Unmarshal([]byte(lines[1]), &responseAuditEvent)
	require.NoError(t, err, "Error parsing JSON data")

	t.Log("Verifying workspace annotation on audit events")
	workspaceNameSent := ws.Spec.Cluster
	require.Equal(t, workspaceNameSent, requestAuditEvent.Annotations["tenancy.kcp.io/workspace"])
	require.Equal(t, workspaceNameSent, responseAuditEvent.Annotations["tenancy.kcp.io/workspace"])

	for _, annotation := range []string{
		"request.auth.kcp.io/01-requiredgroups",
		"request.auth.kcp.io/02-content",
		"request.auth.kcp.io/03-systemcrd",
		"request.auth.kcp.io/04-maxpermissionpolicy",
		"request.auth.kcp.io/05-bootstrap",
	} {
		t.Logf("Verifying authorization annotations on audit events: %s", annotation)
		if _, ok := responseAuditEvent.Annotations[annotation+"-decision"]; !ok {
			t.Errorf("expected annotation %v-decision but got none", annotation)
		}
		if _, ok := responseAuditEvent.Annotations[annotation+"-reason"]; !ok {
			t.Errorf("expected annotation %v-reason but got none", annotation)
		}
	}
}
