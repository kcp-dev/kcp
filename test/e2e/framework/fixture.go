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

package framework

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
)

// KcpFixture manages the lifecycle of a set of kcp servers.
type KcpFixture struct {
	Servers map[string]RunningServer
}

func NewKcpFixture(t *testing.T, cfgs ...KcpConfig) *KcpFixture {
	f := &KcpFixture{}

	artifactDir, dataDir, err := ScratchDirs(t)
	require.NoError(t, err, "failed to create scratch dirs: %v", err)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	// Initialize servers from the provided configuration
	var servers []*kcpServer
	f.Servers = map[string]RunningServer{}
	for _, cfg := range cfgs {
		server, err := newKcpServer(t, cfg, artifactDir, dataDir)
		require.NoError(t, err)

		servers = append(servers, server)
		f.Servers[server.name] = server
	}

	// Launch kcp servers and ensure they are ready before starting the test
	start := time.Now()
	t.Log("Starting kcp servers...")
	wg := sync.WaitGroup{}
	wg.Add(len(servers))
	for i, srv := range servers {
		var opts []RunOption
		if cfgs[i].LogToConsole {
			opts = append(opts, WithLogStreaming)
		}
		if InProcessEnvSet() || cfgs[i].RunInProcess {
			opts = append(opts, RunInProcess)
		}
		err := srv.Run(ctx, opts...)
		require.NoError(t, err)

		// Wait for the server to become ready
		go func(s *kcpServer, i int) {
			defer wg.Done()
			err := s.Ready(!cfgs[i].RunInProcess)
			require.NoError(t, err, "kcp server %s never became ready: %v", s.name, err)
		}(srv, i)
	}
	wg.Wait()

	if t.Failed() {
		t.Fatal("Fixture setup failed: one or more servers did not become ready")
	}

	t.Logf("Started kcp servers after %s", time.Since(start))

	return f
}

func InProcessEnvSet() bool {
	inProcess, _ := strconv.ParseBool(os.Getenv("INPROCESS"))
	return inProcess
}

func NewOrganizationFixture(t *testing.T, server RunningServer) (orgClusterName string) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	cfg, err := server.Config()
	if err != nil {
		t.Fatalf("failed to get kcp server config: %v", err)
	}

	clusterClient, err := kcpclientset.NewClusterForConfig(cfg)
	if err != nil {
		t.Fatalf("failed to construct client for server: %v", err)
	}

	org, err := clusterClient.Cluster(helper.RootCluster).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "e2e-org-",
		},
		Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
			Type: "Organization",
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create organization: %v", err)
	}

	t.Cleanup(func() {
		err := clusterClient.Cluster(helper.RootCluster).TenancyV1alpha1().ClusterWorkspaces().Delete(ctx, org.Name, metav1.DeleteOptions{})
		if err != nil {
			// nolint: nilcheck
			t.Logf("failed to delete organization workspace %s: %v", org.Name, err)
		}
	})

	if err := wait.PollImmediateWithContext(ctx, time.Millisecond*100, wait.ForeverTestTimeout, func(ctx context.Context) (done bool, err error) {
		ws, err := clusterClient.Cluster(helper.RootCluster).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, org.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return ws.Status.Phase == tenancyv1alpha1.ClusterWorkspacePhaseReady, nil
	}); err != nil {
		t.Fatalf("failed to wait for organization workspace %s to become ready: %v", org.Name, err)
	}

	return helper.EncodeOrganizationAndWorkspace(helper.RootCluster, org.Name)
}

func NewWorkspaceFixture(t *testing.T, server RunningServer, orgClusterName string, typ string) (clusterName string) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	rootOrg, orgName, err := helper.ParseLogicalClusterName(orgClusterName)
	if err != nil {
		t.Fatalf("failed to parse organization cluster name %q: %v", orgClusterName, err)
	} else if rootOrg != helper.RootCluster {
		t.Fatalf("expected an org cluster name, i.e. with \"%s:\" prefix, got: %s", helper.RootCluster, orgClusterName)
	}

	cfg, err := server.Config()
	if err != nil {
		t.Fatalf("failed to get server config: %v", err)
	}

	clusterClient, err := kcpclientset.NewClusterForConfig(cfg)
	if err != nil {
		t.Fatalf("failed to construct client for server: %v", err)
	}

	ws, err := clusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "e2e-workspace-",
		},
		Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
			Type: typ,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}

	t.Cleanup(func() {
		err := clusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Delete(ctx, ws.Name, metav1.DeleteOptions{})
		if err != nil {
			// nolint: nilcheck
			t.Logf("failed to delete workspace %s: %v", ws.Name, err)
		}
	})

	if err := wait.PollImmediateWithContext(ctx, time.Millisecond*100, wait.ForeverTestTimeout, func(ctx context.Context) (done bool, err error) {
		ws, err := clusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, ws.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return ws.Status.Phase == tenancyv1alpha1.ClusterWorkspacePhaseReady, nil
	}); err != nil {
		t.Fatalf("failed to wait for workspace %s:%s to become ready: %v", orgName, ws.Name, err)
	}

	return helper.EncodeOrganizationAndWorkspace(orgName, ws.Name)
}
