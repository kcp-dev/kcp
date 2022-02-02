/*
Copyright 2021 The KCP Authors.

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
	"flag"
	"time"

	"github.com/spf13/pflag"

	"github.com/kcp-dev/kcp/pkg/etcd"
	"github.com/kcp-dev/kcp/pkg/reconciler/cluster"
	serveroptions "k8s.io/apiserver/pkg/server/options"
	kubeoptions "k8s.io/kubernetes/pkg/kubeapiserver/options"
)

// DefaultConfig is the default behavior of the KCP server.
func DefaultConfig() *Config {
	return &Config{
		EtcdClientInfo:            etcd.ClientInfo{},
		EtcdDirectory:             "",
		EtcdPeerPort:              "2380",
		EtcdClientPort:            "2379",
		CertKey:                   serveroptions.CertKey{},
		InstallClusterController:  false,
		ClusterControllerOptions:  cluster.DefaultOptions(),
		InstallWorkspaceScheduler: false,
		InstallNamespaceScheduler: false,
		KubeConfigPath:            "admin.kubeconfig",
		Listen:                    ":6443",
		RootDirectory:             ".kcp",
		ProfilerAddress:           "",
		ShardKubeconfigFile:       "",
		EnableSharding:            false,
		Authentication:            kubeoptions.NewBuiltInAuthenticationOptions().WithAll(),
		DiscoveryPollInterval:     60 * time.Second,
	}
}

// Config determines the behavior of the KCP server.
type Config struct {
	EtcdClientInfo            etcd.ClientInfo
	EtcdDirectory             string
	EtcdPeerPort              string
	EtcdClientPort            string
	EtcdWalSizeBytes          int64
	CertKey                   serveroptions.CertKey
	ServerCAFile              string
	InstallClusterController  bool
	ClusterControllerOptions  *cluster.Options
	InstallWorkspaceScheduler bool
	InstallNamespaceScheduler bool
	KubeConfigPath            string
	Listen                    string
	RootDirectory             string
	ProfilerAddress           string
	ShardKubeconfigFile       string
	EnableSharding            bool
	Authentication            *kubeoptions.BuiltInAuthenticationOptions
	DiscoveryPollInterval     time.Duration
}

func BindOptions(c *Config, fs *pflag.FlagSet) *Config {
	fs.AddFlag(pflag.PFlagFromGoFlag(flag.CommandLine.Lookup("v")))
	fs.BoolVar(&c.InstallClusterController, "install-cluster-controller", c.InstallClusterController, "Registers the sample cluster custom resource, and the related controller to allow registering physical clusters")
	fs.BoolVar(&c.InstallWorkspaceScheduler, "install-workspace-scheduler", c.InstallWorkspaceScheduler, "Registers the workspace custom resource, and the related controller to allow scheduling workspaces to shards")
	fs.BoolVar(&c.InstallNamespaceScheduler, "install-namespace-scheduler", c.InstallNamespaceScheduler, "Registers the namespace scheduler to allow scheduling namespaces and resource to physical clusters")
	fs.StringVar(&c.Listen, "listen", c.Listen, "Address:port to bind to")
	fs.StringSliceVar(&c.EtcdClientInfo.Endpoints, "etcd-servers", c.EtcdClientInfo.Endpoints, "List of external etcd servers to connect with (scheme://ip:port), comma separated. If absent an in-process etcd will be created.")
	fs.StringVar(&c.EtcdClientInfo.KeyFile, "etcd-keyfile", c.EtcdClientInfo.KeyFile, "TLS key file used to secure etcd communication.")
	fs.StringVar(&c.EtcdClientInfo.CertFile, "etcd-certfile", c.EtcdClientInfo.CertFile, "TLS certification file used to secure etcd communication.")
	fs.StringVar(&c.EtcdClientInfo.TrustedCAFile, "etcd-cafile", c.EtcdClientInfo.TrustedCAFile, "TLS Certificate Authority file used to secure etcd communication.")
	fs.StringVar(&c.CertKey.KeyFile, "server-keyfile", c.CertKey.KeyFile, "TLS key file used to secure server communication.")
	fs.StringVar(&c.CertKey.CertFile, "server-certfile", c.CertKey.CertFile, "TLS certification file used to secure server communication.")
	fs.StringVar(&c.ProfilerAddress, "profiler-address", c.ProfilerAddress, "[Address]:port to bind the profiler to.")
	fs.StringVar(&c.ShardKubeconfigFile, "shard-kubeconfig-file", c.ShardKubeconfigFile, "Kubeconfig holding admin(!) credentials to peer kcp shards.")
	fs.BoolVar(&c.EnableSharding, "enable-sharding", c.EnableSharding, "Enable delegating to peer kcp shards.")
	fs.StringVar(&c.RootDirectory, "root-directory", c.RootDirectory, "Root directory.")
	fs.StringVar(&c.EtcdPeerPort, "etcd-peer-port", c.EtcdPeerPort, "Port for etcd peer communication.")
	fs.StringVar(&c.EtcdClientPort, "etcd-client-port", c.EtcdClientPort, "Port for etcd client communication.")
	fs.Int64Var(&c.EtcdWalSizeBytes, "etcd-wal-size-bytes", c.EtcdWalSizeBytes, "Size in bytes for the etcd WAL. Leave unset to use the default.")
	fs.DurationVar(&c.DiscoveryPollInterval, "discovery-poll-interval", c.DiscoveryPollInterval, "Polling interval for dynamic discovery informers.")
	fs.StringVar(&c.KubeConfigPath, "kubeconfig-path", c.KubeConfigPath, "Path to which the administrative kubeconfig should be written at startup.")

	c.ClusterControllerOptions = cluster.BindOptions(c.ClusterControllerOptions, fs)

	c.Authentication.AddFlags(fs)
	return c
}
