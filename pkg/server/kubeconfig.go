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

func createKubeConfig(adminUserName, adminBearerToken, baseHost, tlsServerName string, caData []byte) *clientcmdapi.Config {
	var kubeConfig clientcmdapi.Config
	//Create Client and Shared
	kubeConfig.AuthInfos = map[string]*clientcmdapi.AuthInfo{
		adminUserName: {Token: adminBearerToken},
	}
	kubeConfig.Clusters = map[string]*clientcmdapi.Cluster{
		// cross-cluster is the virtual cluster running by default
		"cross-cluster": {
			Server:                   baseHost + "/clusters/*",
			CertificateAuthorityData: caData,
			TLSServerName:            tlsServerName,
		},
		// root is the virtual cluster containing the organizations
		"root": {
			Server:                   baseHost + "/clusters/root",
			CertificateAuthorityData: caData,
			TLSServerName:            tlsServerName,
		},
		// system:admin is the virtual cluster running by default
		"system:admin": {
			Server:                   baseHost,
			CertificateAuthorityData: caData,
			TLSServerName:            tlsServerName,
		},
	}
	kubeConfig.Contexts = map[string]*clientcmdapi.Context{
		"cross-cluster": {Cluster: "cross-cluster", AuthInfo: adminUserName},
		"root":          {Cluster: "root", AuthInfo: adminUserName},
		"system:admin":  {Cluster: "system:admin", AuthInfo: adminUserName},
	}
	return &kubeConfig
}

func createLoopbackBasedKubeConfig(server *server.GenericAPIServer) *clientcmdapi.Config {
	loopbackCfg := server.LoopbackClientConfig
	return createKubeConfig("loopback", loopbackCfg.BearerToken, loopbackCfg.Host, loopbackCfg.ServerName, loopbackCfg.CAData)
}
