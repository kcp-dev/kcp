/*
Copyright 2025 The KCP Authors.

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
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/util/cert"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
)

// NewExternalKCPServer returns a RunningServer for a kubeconfig
// pointing to a kcp instance not managed by the test run. Since the
// kubeconfig is expected to exist prior to running tests against it,
// the configuration can be loaded synchronously and no locking is
// required to subsequently access it.
func NewExternalKCPServer(name, kubeconfigPath string, shardKubeconfigPaths map[string]string, clientCADir string) (RunningServer, error) {
	cfg, err := LoadKubeConfig(kubeconfigPath, "base")
	if err != nil {
		return nil, err
	}

	shardCfgs := map[string]clientcmd.ClientConfig{}
	for shard, path := range shardKubeconfigPaths {
		shardCfg, err := LoadKubeConfig(path, "base")
		if err != nil {
			return nil, err
		}

		shardCfgs[shard] = shardCfg
	}

	return &externalKCPServer{
		name:                 name,
		kubeconfigPath:       kubeconfigPath,
		shardKubeconfigPaths: shardKubeconfigPaths,
		cfg:                  cfg,
		shardCfgs:            shardCfgs,
		caDir:                clientCADir,
	}, nil
}

type externalKCPServer struct {
	name                 string
	kubeconfigPath       string
	shardKubeconfigPaths map[string]string
	cfg                  clientcmd.ClientConfig
	shardCfgs            map[string]clientcmd.ClientConfig
	caDir                string
}

func (s *externalKCPServer) CADirectory() string {
	return s.caDir
}

func (s *externalKCPServer) ClientCAUserConfig(t TestingT, config *rest.Config, name string, groups ...string) *rest.Config {
	return clientCAUserConfig(t, config, s.caDir, name, groups...)
}

func (s *externalKCPServer) Name() string {
	return s.name
}

func (s *externalKCPServer) KubeconfigPath() string {
	return s.kubeconfigPath
}

func (s *externalKCPServer) RawConfig() (clientcmdapi.Config, error) {
	return s.cfg.RawConfig()
}

// BaseConfig returns a rest.Config for the "base" context. Client-side throttling is disabled (QPS=-1).
func (s *externalKCPServer) BaseConfig(t TestingT) *rest.Config {
	t.Helper()

	raw, err := s.cfg.RawConfig()
	require.NoError(t, err)

	config := clientcmd.NewNonInteractiveClientConfig(raw, "base", nil, nil)

	defaultConfig, err := config.ClientConfig()
	require.NoError(t, err)

	wrappedCfg := rest.CopyConfig(defaultConfig)
	wrappedCfg.QPS = -1

	return wrappedCfg
}

// RootShardSystemMasterBaseConfig returns a rest.Config for the "shard-base" context. Client-side throttling is disabled (QPS=-1).
func (s *externalKCPServer) RootShardSystemMasterBaseConfig(t TestingT) *rest.Config {
	t.Helper()

	return s.ShardSystemMasterBaseConfig(t, corev1alpha1.RootShard)
}

// ShardSystemMasterBaseConfig returns a rest.Config for the "shard-base" context of the given shard. Client-side throttling is disabled (QPS=-1).
func (s *externalKCPServer) ShardSystemMasterBaseConfig(t TestingT, shard string) *rest.Config {
	t.Helper()

	cfg, found := s.shardCfgs[shard]
	if !found {
		t.Fatalf("kubeconfig for shard %q not found", shard)
	}

	raw, err := cfg.RawConfig()
	require.NoError(t, err)

	config := clientcmd.NewNonInteractiveClientConfig(raw, "shard-base", nil, nil)

	defaultConfig, err := config.ClientConfig()
	require.NoError(t, err)

	wrappedCfg := rest.CopyConfig(defaultConfig)
	wrappedCfg.QPS = -1

	return wrappedCfg
}

func (s *externalKCPServer) ShardNames() []string {
	return sets.StringKeySet(s.shardCfgs).List()
}

func (s *externalKCPServer) Artifact(t TestingT, producer func() (runtime.Object, error)) {
	t.Helper()
	artifact(t, s, producer)
}

// Stop is a noop to satisfy the RunningServer interface.
func (s *externalKCPServer) Stop() {
	// no-op
}

// Stopped is a noop to satisfy the RunningServer interface.
func (s *externalKCPServer) Stopped() bool {
	return false
}

var ErrEmptyKubeConfig = fmt.Errorf("kubeconfig is empty")

// LoadKubeConfig loads a kubeconfig from disk. This method is
// intended to be common between fixture for servers whose lifecycle
// is test-managed and fixture for servers whose lifecycle is managed
// separately from a test run.
func LoadKubeConfig(kubeconfigPath, contextName string) (clientcmd.ClientConfig, error) {
	fs, err := os.Stat(kubeconfigPath)
	if err != nil {
		return nil, err
	}
	if fs.Size() == 0 {
		return nil, ErrEmptyKubeConfig
	}

	rawConfig, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load admin kubeconfig: %w", err)
	}

	return clientcmd.NewNonInteractiveClientConfig(*rawConfig, contextName, nil, nil), nil
}

// WaitLoadKubeConfig wraps LoadKubeConfig and waits until the context
// is cancelled, two minutes have passed, or the kubeconfig file is
// loaded without error.
func WaitLoadKubeConfig(ctx context.Context, kubeconfigPath, contextName string) (clientcmd.ClientConfig, error) {
	var config clientcmd.ClientConfig
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 2*time.Minute, true,
		func(ctx context.Context) (bool, error) {
			loaded, err := LoadKubeConfig(kubeconfigPath, contextName)
			if err != nil {
				if os.IsNotExist(err) || errors.Is(ErrEmptyKubeConfig, err) {
					return false, nil
				}
				return false, err
			}
			config = loaded
			return true, nil
		},
	); err != nil {
		return nil, err
	}
	return config, nil
}

// clientCAUserConfig returns a config based on a dynamically created client certificate.
// The returned client CA is signed by "test/e2e/framework/client-ca.crt".
func clientCAUserConfig(t TestingT, cfg *rest.Config, clientCAConfigDirectory, username string, groups ...string) *rest.Config {
	t.Helper()
	clientCAName := "client-ca"
	caBytes, err := os.ReadFile(filepath.Join(clientCAConfigDirectory, clientCAName+".crt"))
	require.NoError(t, err, "error reading CA file")
	caKeyBytes, err := os.ReadFile(filepath.Join(clientCAConfigDirectory, clientCAName+".key"))
	require.NoError(t, err, "error reading CA key")
	caCerts, err := cert.ParseCertsPEM(caBytes)
	require.NoError(t, err, "error parsing CA certs")
	caKeys, err := tls.X509KeyPair(caBytes, caKeyBytes)
	require.NoError(t, err, "error parsing CA keys")
	clientPublicKey, clientPrivateKey, err := newRSAKeyPair()
	require.NoError(t, err, "error creating client keys")
	currentTime := time.Now()
	clientCert := &x509.Certificate{
		Subject: pkix.Name{
			CommonName:   username,
			Organization: groups,
		},

		SignatureAlgorithm: x509.SHA256WithRSA,

		NotBefore:    currentTime.Add(-1 * time.Second),
		NotAfter:     currentTime.Add(time.Hour * 2),
		SerialNumber: big.NewInt(1),

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	signedClientCertBytes, err := x509.CreateCertificate(cryptorand.Reader, clientCert, caCerts[0], clientPublicKey, caKeys.PrivateKey)
	require.NoError(t, err, "error creating client certificate")
	clientCertPEM := new(bytes.Buffer)
	require.NoError(t, pem.Encode(clientCertPEM, &pem.Block{Type: "CERTIFICATE", Bytes: signedClientCertBytes}), "error encoding client cert")
	clientKeyPEM := new(bytes.Buffer)
	require.NoError(t, pem.Encode(clientKeyPEM, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(clientPrivateKey)}), "error encoding client private key")

	cfgCopy := rest.CopyConfig(cfg)
	cfgCopy.CertData = clientCertPEM.Bytes()
	cfgCopy.KeyData = clientKeyPEM.Bytes()
	cfgCopy.BearerToken = ""
	return cfgCopy
}

func newRSAKeyPair() (*rsa.PublicKey, *rsa.PrivateKey, error) {
	privateKey, err := rsa.GenerateKey(cryptorand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	return &privateKey.PublicKey, privateKey, nil
}
