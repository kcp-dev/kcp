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

package framework

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kcp-dev/logicalcluster/v3"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"
)

// WriteWorkspaceKubeconfig writes a kubeconfig for wsPath to dir and returns its path.
func WriteWorkspaceKubeconfig(t *testing.T, server kcptestingserver.RunningServer, wsPath logicalcluster.Path, dir string) string {
	t.Helper()

	cfg := server.BaseConfig(t)
	cfg.Host += "/clusters/" + wsPath.String()

	kubeconfig := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"kcp": {
				Server:                   cfg.Host,
				CertificateAuthorityData: cfg.CAData,
				TLSServerName:            cfg.ServerName,
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"kcp": {
				ClientCertificateData: cfg.CertData,
				ClientKeyData:         cfg.KeyData,
				Token:                 cfg.BearerToken,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"kcp": {Cluster: "kcp", AuthInfo: "kcp"},
		},
		CurrentContext: "kcp",
	}

	kubeconfigPath := filepath.Join(dir, "kubeconfig.yaml")
	err := clientcmd.WriteToFile(kubeconfig, kubeconfigPath)
	require.NoError(t, err)

	return kubeconfigPath
}
