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

package customresourcedefinition

import (
	"context"
	"embed"
	"testing"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/stretchr/testify/require"

	kcpapiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/kcp/clientset/versioned"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var testFiles embed.FS

func TestCustomResourceCreation(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgClusterName := framework.NewOrganizationFixture(t, server)
	sourceWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName.Path())

	cfg := server.BaseConfig(t)

	kcpClients, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClients, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClients.Cluster(sourceWorkspace.Path()).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClients.Cluster(sourceWorkspace.Path()), mapper, nil, "wildwest.dev_cowboys.yaml", testFiles)
	if err == nil {
		t.Errorf("Expected an error due to reserved annotation")
	}

	err = helpers.CreateResourceFromFS(ctx, dynamicClients.Cluster(sourceWorkspace.Path()), mapper, nil, "apis.kcp.dev_cowboys.yaml", testFiles)
	if err == nil {
		t.Errorf("Expected an error due to reserved group")
	}
}
