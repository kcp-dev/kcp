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
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/request/bearertoken"
	authenticatorunion "k8s.io/apiserver/pkg/authentication/request/union"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizerfactory"
	authorizationunion "k8s.io/apiserver/pkg/authorization/union"
	"k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const adminTokenStore string = ".admin-token-store"

func setupKubeConfigAdminToken(rootDirectory string, authn *server.AuthenticationInfo, authz *server.AuthorizationInfo) (newAdminToken string, adminTokenHash []byte, err error) {
	tokenHashFileName := filepath.Join(rootDirectory, adminTokenStore)
	adminTokenHash, err = ioutil.ReadFile(tokenHashFileName)
	if os.IsNotExist(err) {
		newAdminToken = uuid.New().String()
		sum := sha256.Sum256([]byte(newAdminToken))
		adminTokenHash = sum[:]
		if err := ioutil.WriteFile(tokenHashFileName, adminTokenHash, 0600); err != nil {
			return "", nil, err
		}
	}

	if authn == nil || authz == nil {
		// prevent nil pointer panic
		return
	}
	if authn.Authenticator == nil || authz.Authorizer == nil {
		// authenticator or authorizer might be nil if we want to bypass authz/authn
		// and we also do nothing in this case.
		return
	}

	var uid = uuid.New().String()
	adminUser := &user.DefaultInfo{
		Name:   user.APIServerUser,
		UID:    uid,
		Groups: []string{user.SystemPrivilegedGroup},
	}

	newAuthenticator := bearertoken.New(authenticator.WrapAudienceAgnosticToken(authn.APIAudiences, authenticator.TokenFunc(func(ctx context.Context, requestToken string) (*authenticator.Response, bool, error) {
		requestTokenHash := sha256.Sum256([]byte(requestToken))
		if !bytes.Equal(requestTokenHash[:], adminTokenHash) {
			return nil, false, nil
		}
		return &authenticator.Response{User: adminUser}, true, nil

	})))

	authn.Authenticator = authenticatorunion.New(newAuthenticator, authn.Authenticator)

	tokenAuthorizer := authorizerfactory.NewPrivilegedGroups(user.SystemPrivilegedGroup)
	authz.Authorizer = authorizationunion.New(tokenAuthorizer, authz.Authorizer)

	return newAdminToken, adminTokenHash, nil
}

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
		// admin is the virtual cluster running by default
		"admin": {
			Server:                   baseHost,
			CertificateAuthorityData: caData,
			TLSServerName:            tlsServerName,
		},
		// user is a virtual cluster that is lazily instantiated
		"user": {
			Server:                   baseHost + "/clusters/user",
			CertificateAuthorityData: caData,
			TLSServerName:            tlsServerName,
		},
	}
	kubeConfig.Contexts = map[string]*clientcmdapi.Context{
		"cross-cluster": {Cluster: "cross-cluster", AuthInfo: adminUserName},
		"admin":         {Cluster: "admin", AuthInfo: adminUserName},
		"user":          {Cluster: "user", AuthInfo: adminUserName},
	}
	kubeConfig.CurrentContext = "admin"
	return &kubeConfig
}

func createKubeConfigs(server *server.GenericAPIServer, externalKubeConfigAdminToken string, externalKubeConfigAdminTokenHash []byte, externalKubeConfigPath string) (loopbackBasedKubeConfig *clientcmdapi.Config, externalKubeConfig *clientcmdapi.Config, err error) {
	externalCACert, _ := server.SecureServingInfo.Cert.CurrentCertKeyContent()
	if err != nil {
		return nil, nil, err
	}
	servingHostName, servingPort, err := server.SecureServingInfo.HostPort()
	if err != nil {
		return nil, nil, err
	}
	if servingHostName == "0.0.0.0" || servingHostName == "::" {
		servingHostName = "localhost"
	}
	externalKubeConfigHost := fmt.Sprintf("https://%s:%d", servingHostName, servingPort)

	loopbackCfg := server.LoopbackClientConfig
	loopbackBasedKubeConfig = createKubeConfig("loopback", loopbackCfg.BearerToken, loopbackCfg.Host, loopbackCfg.ServerName, loopbackCfg.CAData)

	externalAdminUserName := "admin"
	if externalKubeConfigAdminToken == "" {
		// The same token will be used: retrieve it, but only if its hash matches the stored token hash.
		existingExternalKubeConfig, err := clientcmd.LoadFromFile(externalKubeConfigPath)
		if err != nil && !os.IsNotExist(err) {
			return nil, nil, err
		}
		if existingExternalKubeConfig != nil {
			if externalAdminUser := existingExternalKubeConfig.AuthInfos[externalAdminUserName]; externalAdminUser != nil {
				kubeConfigTokenHash := sha256.Sum256([]byte(externalAdminUser.Token))
				if !bytes.Equal(kubeConfigTokenHash[:], externalKubeConfigAdminTokenHash) {
					return nil, nil, fmt.Errorf("admin token in file %s is not valid anymore.\nremove file %s and restart KCP.",
						externalKubeConfigPath,
						filepath.Join(filepath.Dir(externalKubeConfigPath), adminTokenStore),
					)
				}
				externalKubeConfigAdminToken = externalAdminUser.Token
			}
		}
	}
	if externalKubeConfigAdminToken == "" {
		return nil, nil, fmt.Errorf("cannot create the 'admin.kubeconfig` file with an empty token for the %s user", externalAdminUserName)
	}
	externalKubeConfig = createKubeConfig("admin", externalKubeConfigAdminToken, externalKubeConfigHost, "", externalCACert)

	return
}
