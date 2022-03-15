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

package server

import (
	"k8s.io/apiserver/pkg/server"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// TODO(sttts): this is a hack, using the loopback config as a blueprint. Syncer should never use a loopback connection.
func CreateLoopbackUpstreamKubeConfig(server *server.GenericAPIServer) *clientcmdapi.Config {
	return &clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"upstream": {
				Server:                   server.LoopbackClientConfig.Host,
				CertificateAuthorityData: server.LoopbackClientConfig.CAData,
				TLSServerName:            server.LoopbackClientConfig.ServerName,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"upstream": {
				Cluster:  "upstream",
				AuthInfo: "syncer",
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"syncer": {
				Token: server.LoopbackClientConfig.BearerToken,
			},
		},
		CurrentContext: "upstream",
	}
}
