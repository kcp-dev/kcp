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

package options

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io/ioutil"
	"os"

	"github.com/google/uuid"
	"github.com/spf13/pflag"

	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/request/bearertoken"
	authenticatorunion "k8s.io/apiserver/pkg/authentication/request/union"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

type AdminAuthentication struct {
	KubeConfigPath string

	// TODO: move into Secret in-cluster, maybe by using an "in-cluster" string as value
	TokenHashFilePath string
}

func NewAdminAuthentication() *AdminAuthentication {
	return &AdminAuthentication{
		KubeConfigPath:    "admin.kubeconfig",
		TokenHashFilePath: ".admin-token-store",
	}
}

func (s *AdminAuthentication) Validate() []error {
	if s == nil {
		return nil
	}

	errs := []error{}

	if s.TokenHashFilePath == "" && s.KubeConfigPath != "" {
		errs = append(errs, fmt.Errorf("--admin-kubeconfig requires --admin-token-hash-file-path"))
	}

	return errs
}

func (s *AdminAuthentication) AddFlags(fs *pflag.FlagSet) {
	if s == nil {
		return
	}

	fs.StringVar(&s.KubeConfigPath, "kubeconfig-path", s.KubeConfigPath,
		"Path to which the administrative kubeconfig should be written at startup. If this is relative, it is relative to --root-directory.")
	fs.StringVar(&s.TokenHashFilePath, "authentication-admin-token-path", s.TokenHashFilePath,
		"Path to which the administrative token hash should be written at startup. If this is relative, it is relative to --root-directory.")
}

func (s *AdminAuthentication) ApplyTo(config *genericapiserver.Config) (newTokenOrEmpty string, tokenHash []byte, err error) {
	// try to load existing token to reuse
	tokenHash, err = ioutil.ReadFile(s.TokenHashFilePath)
	if os.IsNotExist(err) {
		newTokenOrEmpty = uuid.New().String()
		sum := sha256.Sum256([]byte(newTokenOrEmpty))
		tokenHash = sum[:]
		if err := ioutil.WriteFile(s.TokenHashFilePath, tokenHash, 0600); err != nil {
			return "", nil, err
		}
	}

	var uid = uuid.New().String()
	adminUser := &user.DefaultInfo{
		Name:   user.APIServerUser,
		UID:    uid,
		Groups: []string{user.SystemPrivilegedGroup},
	}

	newAuthenticator := bearertoken.New(authenticator.WrapAudienceAgnosticToken(config.Authentication.APIAudiences, authenticator.TokenFunc(func(ctx context.Context, requestToken string) (*authenticator.Response, bool, error) {
		requestTokenHash := sha256.Sum256([]byte(requestToken))
		if !bytes.Equal(requestTokenHash[:], tokenHash) {
			return nil, false, nil
		}
		return &authenticator.Response{User: adminUser}, true, nil
	})))

	config.Authentication.Authenticator = authenticatorunion.New(newAuthenticator, config.Authentication.Authenticator)

	return newTokenOrEmpty, tokenHash, nil
}

func (s *AdminAuthentication) WriteKubeConfig(config *genericapiserver.Config, newToken string, tokenHash []byte, externalAddress string, servingPort int) error {
	externalCACert, _ := config.SecureServing.Cert.CurrentCertKeyContent()
	externalKubeConfigHost := fmt.Sprintf("https://%s:%d", externalAddress, servingPort)

	externalAdminUserName := "admin"
	if newToken == "" {
		// The same token will be used: retrieve it, but only if its hash matches the stored token hash.
		existingExternalKubeConfig, err := clientcmd.LoadFromFile(s.KubeConfigPath)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if existingExternalKubeConfig != nil {
			if externalAdminUser := existingExternalKubeConfig.AuthInfos[externalAdminUserName]; externalAdminUser != nil {
				kubeConfigTokenHash := sha256.Sum256([]byte(externalAdminUser.Token))
				if !bytes.Equal(kubeConfigTokenHash[:], tokenHash) {
					return fmt.Errorf("admin token in file %s is not valid anymore. Remove file %s and restart KCP", s.KubeConfigPath, s.TokenHashFilePath)
				}
				newToken = externalAdminUser.Token
			}
		}
	}
	if newToken == "" {
		return fmt.Errorf("cannot create the 'admin.kubeconfig` file with an empty token for the %s user", externalAdminUserName)
	}
	externalKubeConfig := createKubeConfig("admin", newToken, externalKubeConfigHost, "", externalCACert)
	return clientcmd.WriteToFile(*externalKubeConfig, s.KubeConfigPath)
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
		"admin":         {Cluster: "system:admin", AuthInfo: adminUserName},
		"user":          {Cluster: "user", AuthInfo: adminUserName},
	}
	kubeConfig.CurrentContext = "admin"
	return &kubeConfig
}
