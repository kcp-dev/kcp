package server

import (
	"flag"

	"github.com/spf13/pflag"

	"github.com/kcp-dev/kcp/pkg/etcd"
)

// Config determines the behavior of the KCP server.
type Config struct {
	AutoPublishAPIs            bool
	EtcdClientInfo             etcd.ClientInfo
	EtcdDirectory              string
	EtcdPeerPort               string
	EtcdClientPort             string
	InstallClusterController   bool
	InstallWorkspaceController bool
	KubeConfigPath             string
	Listen                     string
	PullMode                   bool
	PushMode                   bool
	ResourcesToSync            []string
	RootDirectory              string
	SyncerImage                string
	ProfilerAddress            string
	ShardKubeconfigFile        string
	EnableSharding             bool
	ClientCAFile               string
}

// DefaultConfig returns a configuration with default values.
func DefaultConfig() *Config {
	return &Config{
		AutoPublishAPIs:          false,
		EtcdClientInfo:           etcd.ClientInfo{},
		EtcdDirectory:            "data",
		EtcdPeerPort:             "2380",
		EtcdClientPort:           "2379",
		InstallClusterController: false,
		KubeConfigPath:           "admin.kubeconfig",
		Listen:                   ":6443",
		PullMode:                 false,
		PushMode:                 false,
		ResourcesToSync:          []string{"deployments.apps"},
		RootDirectory:            ".kcp",
		SyncerImage:              "quay.io/kcp-dev/kcp-syncer",
	}
}

// ConfigFromFlags returns a default configuration with config values set from the provided flag set
func ConfigFromFlags(flags *pflag.FlagSet) *Config {
	cfg := DefaultConfig()
	listen, err := flags.GetString("listen")
	if err == nil {
		cfg.Listen = listen
	}
	syncerImage, err := flags.GetString("syncer_image")
	if err == nil {
		cfg.SyncerImage = syncerImage
	}
	resourcesToSync, err := flags.GetStringSlice("resources_to_sync")
	if err == nil {
		cfg.ResourcesToSync = resourcesToSync
	}
	installClusterController, err := flags.GetBool("install_cluster_controller")
	if err == nil {
		cfg.InstallClusterController = installClusterController
	}
	installWorkspaceController, err := flags.GetBool("install_workspace_controller")
	if err == nil {
		cfg.InstallWorkspaceController = installWorkspaceController
	}
	pullMode, err := flags.GetBool("pull_mode")
	if err == nil {
		cfg.PullMode = pullMode
	}
	pushMode, err := flags.GetBool("push_mode")
	if err == nil {
		cfg.PushMode = pushMode
	}
	autoPublishAPIs, err := flags.GetBool("auto_publish_apis")
	if err == nil {
		cfg.AutoPublishAPIs = autoPublishAPIs
	}
	etcdServers, err := flags.GetStringSlice("etcd-servers")
	if err == nil {
		cfg.EtcdClientInfo.Endpoints = etcdServers
	}
	etcdKeyFile, err := flags.GetString("etcd-keyfile")
	if err == nil {
		cfg.EtcdClientInfo.KeyFile = etcdKeyFile
	}
	etcdCertFile, err := flags.GetString("etcd-certfile")
	if err == nil {
		cfg.EtcdClientInfo.CertFile = etcdCertFile
	}
	etcdTrustedCAFile, err := flags.GetString("etcd-cafile")
	if err == nil {
		cfg.EtcdClientInfo.TrustedCAFile = etcdTrustedCAFile
	}
	if profilerAddress, err := flags.GetString("profiler-address"); err == nil {
		cfg.ProfilerAddress = profilerAddress
	}
	shardKubeconfigFile, err := flags.GetString("shard-kubeconfig-file")
	if err == nil {
		cfg.ShardKubeconfigFile = shardKubeconfigFile
	}
	enableSharding, err := flags.GetBool("enable-sharding")
	if err == nil {
		cfg.EnableSharding = enableSharding
	}
	rootDirectory, err := flags.GetString("root_directory")
	if err == nil {
		cfg.RootDirectory = rootDirectory
	}
	etcdPeerPort, err := flags.GetString("etcd_peer_port")
	if err == nil {
		cfg.EtcdPeerPort = etcdPeerPort
	}
	etcdClientPort, err := flags.GetString("etcd_client_port")
	if err == nil {
		cfg.EtcdClientPort = etcdClientPort
	}
	kubeConfigPath, err := flags.GetString("kubeconfig_path")
	if err == nil {
		cfg.KubeConfigPath = kubeConfigPath
	}
	clientCAFile, err := flags.GetString("client-ca-file")
	if err == nil {
		cfg.ClientCAFile = clientCAFile
	}
	return cfg
}

// AddConfigFlags adds all the config flags to the provided flag set
func AddConfigFlags(flags *pflag.FlagSet) {
	flags.AddFlag(pflag.PFlagFromGoFlag(flag.CommandLine.Lookup("v")))
	flags.String("syncer_image", "quay.io/kcp-dev/kcp-syncer", "References a container image that contains syncer and will be used by the syncer POD in registered physical clusters.")
	flags.StringSlice("resources_to_sync", []string{"deployments.apps"}, "Provides the list of resources that should be synced from KCP logical cluster to underlying physical clusters")
	flags.Bool("install_cluster_controller", false, "Registers the sample cluster custom resource, and the related controller to allow registering physical clusters")
	flags.Bool("install_workspace_controller", false, "Registers the workspace custom resource, and the related controller to allow scheduling workspaces to shards")
	flags.Bool("pull_mode", false, "Deploy the syncer in registered physical clusters in POD, and have it sync resources from KCP")
	flags.Bool("push_mode", false, "If true, run syncer for each cluster from inside cluster controller")
	flags.String("listen", ":6443", "Address:port to bind to")
	flags.Bool("auto_publish_apis", false, "If true, the APIs imported from physical clusters will be published automatically as CRDs")
	flags.StringSlice("etcd-servers", []string{},
		"List of external etcd servers to connect with (scheme://ip:port), comma separated. If absent an in-process etcd will be created.")
	flags.String("etcd-keyfile", "",
		"TLS key file used to secure etcd communication.")
	flags.String("etcd-certfile", "",
		"TLS certification file used to secure etcd communication.")
	flags.String("etcd-cafile", "",
		"TLS Certificate Authority file used to secure etcd communication.")
	flags.String("profiler-address", "", "[Address]:port to bind the profiler to.")
	flags.String("shard-kubeconfig-file", "",
		"Kubeconfig holding admin(!) credentials to peer kcp shards.")
	flags.Bool("enable-sharding", false,
		"Enable delegating to peer kcp shards.")
	flags.String("root_directory", ".kcp",
		"Root directory.")
	flags.String("etcd_peer_port", "2380", "Port for etcd peer communication.")
	flags.String("etcd_client_port", "2379", "Port for etcd client communication.")
	flags.String("kubeconfig_path", "admin.kubeconfig", "Path to which the administrative kubeconfig should be written at startup.")
	flags.String("client-ca-file", "", "If set, any request presenting a client certificate signed by one of "+
		"the authorities in the client-ca-file is authenticated with an identity corresponding to the CommonName of the client certificate.")
}
