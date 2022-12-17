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
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/spf13/pflag"

	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/group"
	"k8s.io/apiserver/pkg/authentication/request/bearertoken"
	authenticatorunion "k8s.io/apiserver/pkg/authentication/request/union"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

const (
	// A shard admin being member of the privileged system group.
	// This will bypass most kcp authorization checks.
	shardAdminUserName = "shard-admin"
	// A kcp admin being member of system:kcp:clusterworkspace:admin and system:kcp:clusterworkspace:access.
	// This user will be subject of kcp authorization checks.
	kcpAdminUserName = "kcp-admin"
	// A non-admin user part of the "user" battery.
	kcpUserUserName = "user"
)

type AdminAuthentication struct {
	KubeConfigPath string

	// TODO: move into Secret in-cluster, maybe by using an "in-cluster" string as value
	ShardAdminTokenHashFilePath string
}

func NewAdminAuthentication(rootDir string) *AdminAuthentication {
	return &AdminAuthentication{
		KubeConfigPath:              filepath.Join(rootDir, "admin.kubeconfig"),
		ShardAdminTokenHashFilePath: filepath.Join(rootDir, ".admin-token-store"),
	}
}

func (s *AdminAuthentication) Validate() []error {
	if s == nil {
		return nil
	}

	errs := []error{}

	if s.ShardAdminTokenHashFilePath == "" && s.KubeConfigPath != "" {
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
	fs.StringVar(&s.ShardAdminTokenHashFilePath, "authentication-admin-token-path", s.ShardAdminTokenHashFilePath,
		"Path to which the administrative token hash should be written at startup. If this is relative, it is relative to --root-directory.")
}

// ApplyTo returns a new volatile kcp admin token.
// It also returns a new shard admin token and its hash if the configured shard admin hash file is not present.
// If the shard admin hash file is present only the shard admin hash is returned and the returned shard admin token is empty.
func (s *AdminAuthentication) ApplyTo(config *genericapiserver.Config) (volatileKcpAdminToken, shardAdminToken, volatileUserToken string, shardAdminTokenHash []byte, err error) {
	// try to load existing token to reuse
	shardAdminTokenHash, err = os.ReadFile(s.ShardAdminTokenHashFilePath)
	if os.IsNotExist(err) {
		shardAdminToken = uuid.New().String()
		sum := sha256.Sum256([]byte(shardAdminToken))
		shardAdminTokenHash = sum[:]
		if err := os.WriteFile(s.ShardAdminTokenHashFilePath, shardAdminTokenHash, 0600); err != nil {
			return "", "", "", nil, err
		}
	}

	volatileKcpAdminToken = uuid.New().String()
	volatileUserToken = uuid.New().String()

	shardAdminUser := &user.DefaultInfo{
		Name:   shardAdminUserName,
		UID:    uuid.New().String(),
		Groups: []string{user.SystemPrivilegedGroup},
	}

	kcpAdminUser := &user.DefaultInfo{
		Name: kcpAdminUserName,
		UID:  uuid.New().String(),
		Groups: []string{
			bootstrap.SystemKcpAdminGroup,
		},
	}

	nonAdminUser := &user.DefaultInfo{
		Name:   kcpUserUserName,
		UID:    uuid.New().String(),
		Groups: []string{},
	}

	newAuthenticator := group.NewAuthenticatedGroupAdder(bearertoken.New(authenticator.WrapAudienceAgnosticToken(config.Authentication.APIAudiences, authenticator.TokenFunc(func(ctx context.Context, requestToken string) (*authenticator.Response, bool, error) {
		requestTokenHash := sha256.Sum256([]byte(requestToken))
		if bytes.Equal(requestTokenHash[:], shardAdminTokenHash) {
			return &authenticator.Response{User: shardAdminUser}, true, nil
		}

		if requestToken == volatileKcpAdminToken {
			return &authenticator.Response{User: kcpAdminUser}, true, nil
		}

		if requestToken == volatileUserToken {
			return &authenticator.Response{User: nonAdminUser}, true, nil
		}

		return nil, false, nil
	}))))

	config.Authentication.Authenticator = authenticatorunion.New(newAuthenticator, config.Authentication.Authenticator)

	return volatileKcpAdminToken, shardAdminToken, volatileUserToken, shardAdminTokenHash, nil
}

func (s *AdminAuthentication) WriteKubeConfig(config genericapiserver.CompletedConfig, kcpAdminToken, shardAdminToken, userToken string, shardAdminTokenHash []byte) error {
	externalCACert, _ := config.SecureServing.Cert.CurrentCertKeyContent()
	externalKubeConfigHost := fmt.Sprintf("https://%s", config.ExternalAddress)

	if shardAdminToken == "" { // shard admin token has been generated previously, we have to read it from kubeconfig
		// The same token will be used: retrieve it, but only if its hash matches the stored token hash.
		existingExternalKubeConfig, err := clientcmd.LoadFromFile(s.KubeConfigPath)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if existingExternalKubeConfig != nil {
			if shardAdminAuth := existingExternalKubeConfig.AuthInfos[shardAdminUserName]; shardAdminAuth != nil {
				kubeConfigTokenHash := sha256.Sum256([]byte(shardAdminAuth.Token))
				if !bytes.Equal(kubeConfigTokenHash[:], shardAdminTokenHash) {
					return fmt.Errorf("admin token in file %q is not valid anymore. Remove file %q and restart KCP", s.KubeConfigPath, s.ShardAdminTokenHashFilePath)
				}

				shardAdminToken = shardAdminAuth.Token
			}
		}
	}

	// give up: either there is no new generated shard admin token or the kubeconfig file is malicious.
	if shardAdminToken == "" {
		return fmt.Errorf("cannot create the 'admin.kubeconfig' file with an empty token for the %s user", shardAdminUserName)
	}

	externalKubeConfig := createKubeConfig(kcpAdminToken, shardAdminToken, userToken, externalKubeConfigHost, "", externalCACert)
	return clientcmd.WriteToFile(*externalKubeConfig, s.KubeConfigPath)
}

func createKubeConfig(kcpAdminToken, shardAdminToken, userToken, baseHost, tlsServerName string, caData []byte) *clientcmdapi.Config {
	var kubeConfig clientcmdapi.Config
	// Create Client and Shared
	kubeConfig.AuthInfos = map[string]*clientcmdapi.AuthInfo{
		kcpAdminUserName:   {Token: kcpAdminToken},
		shardAdminUserName: {Token: shardAdminToken},
	}
	kubeConfig.Clusters = map[string]*clientcmdapi.Cluster{
		"root": {
			Server:                   baseHost + "/clusters/root",
			CertificateAuthorityData: caData,
			TLSServerName:            tlsServerName,
		},
		"base": {
			Server:                   baseHost,
			CertificateAuthorityData: caData,
			TLSServerName:            tlsServerName,
		},
	}
	kubeConfig.Contexts = map[string]*clientcmdapi.Context{
		"root":         {Cluster: "root", AuthInfo: kcpAdminUserName},
		"base":         {Cluster: "base", AuthInfo: kcpAdminUserName},
		"system:admin": {Cluster: "base", AuthInfo: shardAdminUserName},
	}
	kubeConfig.CurrentContext = "root"

	if len(userToken) > 0 {
		kubeConfig.AuthInfos[kcpUserUserName] = &clientcmdapi.AuthInfo{Token: userToken}
		kubeConfig.Contexts[kcpUserUserName] = &clientcmdapi.Context{Cluster: "root", AuthInfo: kcpUserUserName}
	}

	return &kubeConfig
}
