package server

import (
	"flag"

	"github.com/spf13/pflag"

	"github.com/kcp-dev/kcp/pkg/etcd"
	"github.com/kcp-dev/kcp/pkg/reconciler/cluster"
)

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

func BindOptions(fs *pflag.FlagSet) *Config {
	c := Config{EtcdClientInfo: etcd.ClientInfo{}}

	fs.AddFlag(pflag.PFlagFromGoFlag(flag.CommandLine.Lookup("v")))
	fs.BoolVar(&c.InstallClusterController, "install_cluster_controller", false, "Registers the sample cluster custom resource, and the related controller to allow registering physical clusters")
	fs.BoolVar(&c.InstallWorkspaceController, "install_workspace_controller", false, "Registers the workspace custom resource, and the related controller to allow scheduling workspaces to shards")
	fs.StringVar(&c.Listen, "listen", ":6443", "Address:port to bind to")
	fs.StringSliceVar(&c.EtcdClientInfo.Endpoints, "etcd-servers", []string{}, "List of external etcd servers to connect with (scheme://ip:port), comma separated. If absent an in-process etcd will be created.")
	fs.StringVar(&c.EtcdClientInfo.KeyFile, "etcd-keyfile", "", "TLS key file used to secure etcd communication.")
	fs.StringVar(&c.EtcdClientInfo.CertFile, "etcd-certfile", "", "TLS certification file used to secure etcd communication.")
	fs.StringVar(&c.EtcdClientInfo.TrustedCAFile, "etcd-cafile", "", "TLS Certificate Authority file used to secure etcd communication.")
	fs.StringVar(&c.ProfilerAddress, "profiler-address", "", "[Address]:port to bind the profiler to.")
	fs.StringVar(&c.ShardKubeconfigFile, "shard-kubeconfig-file", "", "Kubeconfig holding admin(!) credentials to peer kcp shards.")
	fs.BoolVar(&c.EnableSharding, "enable-sharding", false, "Enable delegating to peer kcp shards.")
	fs.StringVar(&c.RootDirectory, "root_directory", ".kcp", "Root directory.")
	fs.StringVar(&c.EtcdPeerPort, "etcd_peer_port", "2380", "Port for etcd peer communication.")
	fs.StringVar(&c.EtcdClientPort, "etcd_client_port", "2379", "Port for etcd client communication.")
	fs.StringVar(&c.KubeConfigPath, "kubeconfig_path", "admin.kubeconfig", "Path to which the administrative kubeconfig should be written at startup.")

	c.ClusterControllerOptions = cluster.BindOptions(fs)

	return &c
}
