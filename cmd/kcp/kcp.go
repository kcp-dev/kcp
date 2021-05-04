package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"go.etcd.io/etcd/clientv3"

	"github.com/kcp-dev/kcp/pkg/cmd/help"
	"github.com/kcp-dev/kcp/pkg/etcd"
	"github.com/kcp-dev/kcp/pkg/reconciler/cluster"

	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/storage/storagebackend"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/kubernetes/pkg/controlplane"
	"k8s.io/kubernetes/pkg/controlplane/clientutils"
	"k8s.io/kubernetes/pkg/controlplane/options"
)

func main() {
	help.FitTerminal()
	cmd := &cobra.Command{
		Use:   "kcp",
		Short: "Kube for Control Plane (KCP)",
		Long: help.Doc(`
			KCP is the easiest way to manage Kubernetes applications against one or
			more clusters, by giving you a personal control plane that schedules your
			workloads onto one or many clusters, and making it simple to pick up and
			move. Advanced use cases including spreading your apps across clusters for
			resiliency, scheduling batch workloads onto clusters with free capacity,
			and enabling collaboration for individual teams without having access to
			the underlying clusters.

			To get started, launch a new cluster with 'kcp start', which will
			initialize your personal control plane and write an admin kubeconfig file
			to disk.
		`),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	var syncerImage *string
	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Start the control plane process",
		Long: help.Doc(`
			Start the control plane process

			The server process listens on port 6443 and will act like a Kubernetes
			API server. It will initialize any necessary data to the provided start
			location or as a '.kcp' directory in the current directory. An admin
			kubeconfig file will be generated at initialization time that may be
			used to access the control plane.
		`),
		RunE: func(cmd *cobra.Command, args []string) error {
			//flag.CommandLine.Lookup("v").Value.Set("9")

			dir := filepath.Join(".", ".kcp")
			if fi, err := os.Stat(dir); err != nil {
				if !os.IsNotExist(err) {
					return err
				}
				if err := os.Mkdir(dir, 0755); err != nil {
					return err
				}
			} else {
				if !fi.IsDir() {
					return fmt.Errorf("%q is a file, please delete or select another location", dir)
				}
			}
			s := &etcd.Server{
				Dir: filepath.Join(dir, "data"),
			}
			ctx := context.TODO()

			return s.Run(func(cfg etcd.ClientInfo) error {
				c, err := clientv3.New(clientv3.Config{
					Endpoints: cfg.Endpoints,
					TLS:       cfg.TLS,
				})
				if err != nil {
					return err
				}
				defer c.Close()
				r, err := c.Cluster.MemberList(context.Background())
				if err != nil {
					return err
				}
				for _, member := range r.Members {
					fmt.Fprintf(os.Stderr, "Connected to etcd %d %s\n", member.GetID(), member.GetName())
				}

				serverOptions := options.NewServerRunOptions()
				serverOptions.SecureServing.ServerCert.CertDirectory = s.Dir
				serverOptions.InsecureServing = nil
				serverOptions.Etcd.StorageConfig.Transport = storagebackend.TransportConfig{
					ServerList:    cfg.Endpoints,
					CertFile:      cfg.CertFile,
					KeyFile:       cfg.KeyFile,
					TrustedCAFile: cfg.TrustedCAFile,
				}
				cpOptions, err := controlplane.Complete(serverOptions)
				if err != nil {
					return err
				}

				server, err := controlplane.CreateServerChain(cpOptions, ctx.Done())
				if err != nil {
					return err
				}

				var clientConfig clientcmdapi.Config
				clientConfig.AuthInfos = map[string]*clientcmdapi.AuthInfo{
					"loopback": {Token: server.LoopbackClientConfig.BearerToken},
				}
				clientConfig.Clusters = map[string]*clientcmdapi.Cluster{
					// admin is the virtual cluster running by default
					"admin": {
						Server:                   server.LoopbackClientConfig.Host,
						CertificateAuthorityData: server.LoopbackClientConfig.CAData,
						TLSServerName:            server.LoopbackClientConfig.TLSClientConfig.ServerName,

					},
					// user is a virtual cluster that is lazily instantiated
					"user": {
						Server:                   server.LoopbackClientConfig.Host + "/clusters/user",
						CertificateAuthorityData: server.LoopbackClientConfig.CAData,
						TLSServerName:            server.LoopbackClientConfig.TLSClientConfig.ServerName,
					},
				}
				clientConfig.Contexts = map[string]*clientcmdapi.Context{
					"admin": {Cluster: "admin", AuthInfo: "loopback"},
					"user":  {Cluster: "user", AuthInfo: "loopback"},
				}
				clientConfig.CurrentContext = "admin"
				if err := clientcmd.WriteToFile(clientConfig, filepath.Join(s.Dir, "admin.kubeconfig")); err != nil {
					return err
				}

				server.AddPostStartHook("Install Cluster Controller", func(context genericapiserver.PostStartHookContext) error {
					// Register the `clusters` CRD in both the admin and user logical clusters
					
					for contextName, _ := range clientConfig.Contexts {
						logicalClusterConfig, err := clientcmd.NewNonInteractiveClientConfig(clientConfig, contextName, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
						if err != nil {
							return err
						}
						cluster.RegisterClusterCRD(logicalClusterConfig)
					}
					adminConfig, err := clientcmd.NewNonInteractiveClientConfig(clientConfig, "admin", &clientcmd.ConfigOverrides{}, nil).ClientConfig()
					if err != nil {
						return err
					}

					kubeconfig := clientConfig.DeepCopy()
					for _, cluster := range kubeconfig.Clusters {
						hostURL, err := url.Parse(cluster.Server)
						if err != nil {
							return err
						}
						hostURL.Host = server.ExternalAddress
						cluster.Server = hostURL.String()
					}

					clientutils.EnableMultiCluster(adminConfig, nil, "clusters", "customresourcedefinitions")
					clusterController := cluster.NewController(
						adminConfig,
						*syncerImage,
						*kubeconfig,
					)
					clusterController.Start(2)
					return nil
				})

				prepared := server.PrepareRun()

				return prepared.Run(ctx.Done())
			})
		},
	}
	startCmd.Flags().AddFlag(pflag.PFlagFromGoFlag(flag.CommandLine.Lookup("v")))
	syncerImage = startCmd.Flags().String("syncer-image", "quay.io/dfestal/kcp-syncer", "syncer-image is the reference of a container image that containers th syncer and will be used by the syncer POD in registered physical clusters.")
	cmd.AddCommand(startCmd)

	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
}
