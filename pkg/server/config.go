package server

import (
	"flag"

	"github.com/spf13/pflag"

	"github.com/kcp-dev/kcp/pkg/etcd"
	"github.com/kcp-dev/kcp/pkg/reconciler/cluster"
)

// DefaultConfig is the default behavior of the KCP server.
func DefaultConfig() *Config {
	return &Config{
		EtcdClientInfo:             etcd.ClientInfo{},
		EtcdDirectory:              "",
		EtcdPeerPort:               "2380",
		EtcdClientPort:             "2379",
		InstallClusterController:   false,
		ClusterControllerOptions:   cluster.DefaultOptions(),
		InstallWorkspaceController: false,
		KubeConfigPath:             "admin.kubeconfig",
		Listen:                     ":6443",
		RootDirectory:              ".kcp",
		ProfilerAddress:            "",
		ShardKubeconfigFile:        "",
		EnableSharding:             false,
	}
}

// Config determines the behavior of the KCP server.
type Config struct {
	EtcdClientInfo             etcd.ClientInfo
	EtcdDirectory              string
	EtcdPeerPort               string
	EtcdClientPort             string
	InstallClusterController   bool
	ClusterControllerOptions   *cluster.Options
	InstallWorkspaceController bool
	KubeConfigPath             string
	Listen                     string
	RootDirectory              string
	ProfilerAddress            string
	ShardKubeconfigFile        string
	EnableSharding             bool
}

func BindOptions(c *Config, fs *pflag.FlagSet) *Config {
	fs.AddFlag(pflag.PFlagFromGoFlag(flag.CommandLine.Lookup("v")))
	fs.BoolVar(&c.InstallClusterController, "install_cluster_controller", c.InstallClusterController, "Registers the sample cluster custom resource, and the related controller to allow registering physical clusters")
	fs.BoolVar(&c.InstallWorkspaceController, "install_workspace_controller", c.InstallWorkspaceController, "Registers the workspace custom resource, and the related controller to allow scheduling workspaces to shards")
	fs.StringVar(&c.Listen, "listen", c.Listen, "Address:port to bind to")
	fs.StringSliceVar(&c.EtcdClientInfo.Endpoints, "etcd-servers", c.EtcdClientInfo.Endpoints, "List of external etcd servers to connect with (scheme://ip:port), comma separated. If absent an in-process etcd will be created.")
	fs.StringVar(&c.EtcdClientInfo.KeyFile, "etcd-keyfile", c.EtcdClientInfo.KeyFile, "TLS key file used to secure etcd communication.")
	fs.StringVar(&c.EtcdClientInfo.CertFile, "etcd-certfile", c.EtcdClientInfo.CertFile, "TLS certification file used to secure etcd communication.")
	fs.StringVar(&c.EtcdClientInfo.TrustedCAFile, "etcd-cafile", c.EtcdClientInfo.TrustedCAFile, "TLS Certificate Authority file used to secure etcd communication.")
	fs.StringVar(&c.ProfilerAddress, "profiler-address", c.ProfilerAddress, "[Address]:port to bind the profiler to.")
	fs.StringVar(&c.ShardKubeconfigFile, "shard-kubeconfig-file", c.ShardKubeconfigFile, "Kubeconfig holding admin(!) credentials to peer kcp shards.")
	fs.BoolVar(&c.EnableSharding, "enable-sharding", c.EnableSharding, "Enable delegating to peer kcp shards.")
	fs.StringVar(&c.RootDirectory, "root_directory", c.RootDirectory, "Root directory.")
	fs.StringVar(&c.EtcdPeerPort, "etcd_peer_port", c.EtcdPeerPort, "Port for etcd peer communication.")
	fs.StringVar(&c.EtcdClientPort, "etcd_client_port", c.EtcdClientPort, "Port for etcd client communication.")
	fs.StringVar(&c.KubeConfigPath, "kubeconfig_path", c.KubeConfigPath, "Path to which the administrative kubeconfig should be written at startup.")

	c.ClusterControllerOptions = cluster.BindOptions(c.ClusterControllerOptions, fs)

	return c
}
