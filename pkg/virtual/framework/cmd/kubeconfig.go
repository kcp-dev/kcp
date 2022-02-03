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

package cmd

import (
	"fmt"
	"path/filepath"

	"k8s.io/apimachinery/pkg/util/sets"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kcp-dev/kcp/pkg/virtual/framework"
)

func (o *APIServerOptions) writeKubeConfig(server *genericapiserver.GenericAPIServer, virtualWorkspaces []framework.VirtualWorkspace) error {
	var clientConfig clientcmdapi.Config

	contextNames := sets.NewString()
	clientConfig.Clusters = map[string]*clientcmdapi.Cluster{}
	clientConfig.Contexts = map[string]*clientcmdapi.Context{}

	for _, vw := range virtualWorkspaces {
		for contextName, path := range vw.GetKubeContextPaths() {
			if contextNames.Has(contextName) {
				return fmt.Errorf("Duplicate Kubeconfig context names: %s", contextName)
			}
			clientConfig.Clusters[contextName] = &clientcmdapi.Cluster{
				Server:                   server.LoopbackClientConfig.Host + path,
				CertificateAuthorityData: server.LoopbackClientConfig.CAData,
				TLSServerName:            server.LoopbackClientConfig.TLSClientConfig.ServerName,
			}
			clientConfig.Contexts[contextName] = &clientcmdapi.Context{Cluster: contextName}
			contextNames.Insert(contextName)
		}
	}
	clientConfig.CurrentContext = o.SubCommandOptions.GetCurrentKubeContext()

	if err := clientcmd.WriteToFile(clientConfig, filepath.Join(o.RootDirectory, o.KubeConfigPath)); err != nil {
		return err
	}
	return nil
}
