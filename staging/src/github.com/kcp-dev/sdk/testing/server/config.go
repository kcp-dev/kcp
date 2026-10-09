/*
Copyright 2025 The kcp Authors.

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
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/component-base/featuregate"

	"github.com/kcp-dev/sdk/testing/third_party/library-go/crypto"
)

// Config qualify a kcp server to start
//
// Deprecated for use outside this package. Prefer PrivateKcpServer().
type Config struct {
	Name        string
	Args        []string
	ArtifactDir string
	DataDir     string
	ClientCADir string
	BindAddress string
	Features    featuregate.MutableFeatureGate

	LogToConsole bool
	RunInProcess bool
}

func (c Config) KubeconfigPath() string {
	return filepath.Join(c.DataDir, "admin.kubeconfig")
}

func (c Config) BuildArgs(t TestingT) ([]string, error) {
	kcpListenPort, err := GetFreePort(t)
	if err != nil {
		return nil, err
	}
	etcdClientPort, err := GetFreePort(t)
	if err != nil {
		return nil, err
	}
	etcdPeerPort, err := GetFreePort(t)
	if err != nil {
		return nil, err
	}

	args := []string{
		"--root-directory", c.DataDir,
		"--secure-port=" + kcpListenPort,
		"--embedded-etcd-client-port=" + etcdClientPort,
		"--embedded-etcd-peer-port=" + etcdPeerPort,
		"--embedded-etcd-wal-size-bytes=" + strconv.Itoa(5*1000), // 5KB
		"--kubeconfig-path=" + c.KubeconfigPath(),
		"--audit-log-path", filepath.Join(c.ArtifactDir, "kcp.audit"),
		"--v=4",
	}

	if c.Features != nil {
		args = append(args, "--feature-gates="+fmt.Sprintf("%s", c.Features))
	}

	if c.BindAddress != "" {
		args = append(args, "--bind-address="+c.BindAddress)
	}

	args = append(args, c.Args...)

	if !hasFlag(args, "--requestheader-client-ca-file") {
		mountArgs, err := mountProxyArgs(t, c.DataDir)
		if err != nil {
			return nil, err
		}
		args = append(args, mountArgs...)
	}

	return args, nil
}

// hasFlag returns true if args contains the given flag, either as "--flag value"
// or as "--flag=value".
func hasFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag || strings.HasPrefix(arg, flag+"=") {
			return true
		}
	}
	return false
}

// mountProxyArgs creates a requestheader CA and a client certificate for the
// shard's mount proxy and returns the kcp flags that wire them up.
//
// A request for a mounted workspace is forwarded with the caller's identity in
// X-Remote-* headers only, which the target trusts solely when they arrive over
// a client certificate signed by its requestheader CA. In the test fixture
// mounts point back at the shard itself, so the shard needs to trust its own
// mount proxy certificate, and to trust the self-signed serving certificate it
// generates in its root directory (dataDir) on start.
func mountProxyArgs(t TestingT, dataDir string) ([]string, error) {
	dir := t.TempDir()
	requestHeaderCA, err := crypto.MakeSelfSignedCA(
		filepath.Join(dir, "requestheader-ca.crt"),
		filepath.Join(dir, "requestheader-ca.key"),
		filepath.Join(dir, "requestheader-ca-serial.txt"),
		"kcp-requestheader-ca",
		365,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create requestheader CA: %w", err)
	}
	if _, err := requestHeaderCA.MakeClientCertificate(
		filepath.Join(dir, "mounts-proxy.crt"),
		filepath.Join(dir, "mounts-proxy.key"),
		&user.DefaultInfo{Name: "kcp-mounts-proxy"},
		365,
	); err != nil {
		return nil, fmt.Errorf("failed to create mount proxy client certificate: %w", err)
	}

	return []string{
		"--requestheader-client-ca-file", filepath.Join(dir, "requestheader-ca.crt"),
		"--requestheader-username-headers=X-Remote-User",
		"--requestheader-group-headers=X-Remote-Group",
		"--requestheader-extra-headers-prefix=X-Remote-Extra-",
		"--requestheader-allowed-names=kcp-mounts-proxy",
		"--mount-proxy-client-cert-file", filepath.Join(dir, "mounts-proxy.crt"),
		"--mount-proxy-client-key-file", filepath.Join(dir, "mounts-proxy.key"),
		"--mount-proxy-server-ca-file", filepath.Join(dataDir, "apiserver.crt"),
	}, nil
}

// Option a function that wish to modify a given kcp configuration.
type Option func(*Config)

// WithDefaultsFrom sets defaults on Config based off of the passed
// TestingT.
func WithDefaultsFrom(t TestingT) Option {
	return func(cfg *Config) {
		cfg.Name = t.Name()
		cfg.ArtifactDir = filepath.Join(t.TempDir(), "artifacts")
		cfg.DataDir = filepath.Join(t.TempDir(), "artifacts")
		cfg.ClientCADir = filepath.Join(t.TempDir(), "certs")
	}
}

// WithScratchDirectories adds custom scratch directories to a kcp configuration.
func WithScratchDirectories(artifactDir, dataDir string) Option {
	return func(cfg *Config) {
		cfg.ArtifactDir = artifactDir
		cfg.DataDir = dataDir
	}
}

// WithCustomArguments applies provided arguments to a given kcp configuration.
func WithCustomArguments(args ...string) Option {
	return func(cfg *Config) {
		cfg.Args = append(cfg.Args, args...)
	}
}

// WithFeatures configures one or more features.
func WithFeatures(m map[string]bool) Option {
	return func(cfg *Config) {
		if cfg.Features == nil {
			cfg.Features = featuregate.NewFeatureGate()
		}

		if err := cfg.Features.SetFromMap(m); err != nil {
			panic(fmt.Sprintf("Failed to set features: %v", err))
		}
	}
}

// WithClientCA sets the client CA directory for a given kcp configuration.
// A client CA will automatically created and the --client-ca configured.
func WithClientCA(clientCADir string) Option {
	return func(cfg *Config) {
		cfg.ClientCADir = clientCADir
	}
}

// WithRunInProcess sets the kcp server to run in process. This requires extra
// setup of the RunInProcessFunc variable and will only work inside of the kcp
// repository.
func WithRunInProcess() Option {
	return func(cfg *Config) {
		cfg.RunInProcess = true
	}
}

// WithLogToConsole sets the kcp server to log to console.
func WithLogToConsole() Option {
	return func(cfg *Config) {
		cfg.LogToConsole = true
	}
}

// WithBindAddress sets the kcp server to log to console.
func WithBindAddress(addr string) Option {
	return func(cfg *Config) {
		cfg.BindAddress = addr
	}
}
