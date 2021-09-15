package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	clientv3 "go.etcd.io/etcd/client/v3"

	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/storage/storagebackend"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/kubernetes/pkg/genericcontrolplane"
	"k8s.io/kubernetes/pkg/genericcontrolplane/clientutils"
	"k8s.io/kubernetes/pkg/genericcontrolplane/options"

	"github.com/kcp-dev/kcp/pkg/etcd"
	"github.com/kcp-dev/kcp/pkg/reconciler/apiresource"
	"github.com/kcp-dev/kcp/pkg/reconciler/cluster"
)

// Server manages the configuration and kcp api-server. It allows callers to easily use kcp
// as a library rather than as a single binary. Using its constructor function, you can easily
// setup a new api-server and start it:
//
//   srv := server.NewServer(server.DefaultConfig())
//   srv.Run(ctx)
//
// You may optionally provide PostStartHookFunc and PreShutdownHookFunc hooks before starting
// the server that should be passed to the api-server itself. These hooks have access to a
// restclient.Config which allows you to easily create a client.
//
//   srv.AddPostStartHook("my-hook", func(context genericapiserver.PostStartHookContext) error {
//       client := clientset.NewForConfigOrDie(context.LoopbackClientConfig)
//   })
type Server struct {
	cfg              *Config
	postStartHooks   []postStartHookEntry
	preShutdownHooks []preShutdownHookEntry
}

// postStartHookEntry groups a PostStartHookFunc with a name. We're not storing these hooks
// in a map and are instead letting the underlying api server perform the hook validation,
// such as checking for multiple PostStartHookFunc with the same name
type postStartHookEntry struct {
	name string
	hook genericapiserver.PostStartHookFunc
}

// preShutdownHookEntry fills the same purpose as postStartHookEntry except that it handles
// the PreShutdownHookFunc
type preShutdownHookEntry struct {
	name string
	hook genericapiserver.PreShutdownHookFunc
}

// Run starts the KCP api-server. This function blocks until the api-server stops or an error.
func (s *Server) Run(ctx context.Context) error {
	dir := filepath.Join(".", s.cfg.RootDirectory)
	if len(s.cfg.RootDirectory) != 0 {
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
	}
	es := &etcd.Server{
		Dir: filepath.Join(dir, s.cfg.EtcdDirectory),
	}

	runFunc := func(cfg etcd.ClientInfo) error {
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
		host, port, err := net.SplitHostPort(s.cfg.Listen)
		if err != nil {
			return fmt.Errorf("--listen must be of format host:port: %w", err)
		}

		if host != "" {
			serverOptions.SecureServing.BindAddress = net.ParseIP(host)
		}
		if port != "" {
			p, err := strconv.Atoi(port)
			if err != nil {
				return err
			}
			serverOptions.SecureServing.BindPort = p
		}

		serverOptions.SecureServing.ServerCert.CertDirectory = es.Dir
		serverOptions.Etcd.StorageConfig.Transport = storagebackend.TransportConfig{
			ServerList:    cfg.Endpoints,
			CertFile:      cfg.CertFile,
			KeyFile:       cfg.KeyFile,
			TrustedCAFile: cfg.TrustedCAFile,
		}
		cpOptions, err := genericcontrolplane.Complete(serverOptions)
		if err != nil {
			return err
		}

		server, err := genericcontrolplane.CreateServerChain(cpOptions, ctx.Done())
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
		if err := clientcmd.WriteToFile(clientConfig, filepath.Join(s.cfg.RootDirectory, s.cfg.KubeConfigPath)); err != nil {
			return err
		}

		// Add our custom hooks to the underlying api server
		for _, entry := range s.postStartHooks {
			err := server.AddPostStartHook(entry.name, entry.hook)
			if err != nil {
				return err
			}
		}
		for _, entry := range s.preShutdownHooks {
			err := server.AddPreShutdownHook(entry.name, entry.hook)
			if err != nil {
				return err
			}
		}

		if s.cfg.InstallClusterController {
			if err := server.AddPostStartHook("Install Cluster Controller", func(context genericapiserver.PostStartHookContext) error {
				// Register the `clusters` CRD in both the admin and user logical clusters
				for contextName := range clientConfig.Contexts {
					logicalClusterConfig, err := clientcmd.NewNonInteractiveClientConfig(clientConfig, contextName, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
					if err != nil {
						return err
					}
					if err := cluster.RegisterCRDs(logicalClusterConfig); err != nil {
						return err
					}
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

				if s.cfg.PullMode && s.cfg.PushMode {
					return errors.New("can't set --push_mode and --pull_mode")
				}
				syncerMode := cluster.SyncerModeNone
				if s.cfg.PullMode {
					syncerMode = cluster.SyncerModePull
				}
				if s.cfg.PushMode {
					syncerMode = cluster.SyncerModePush
				}

				clientutils.EnableMultiCluster(adminConfig, nil, "clusters", "customresourcedefinitions", "apiresourceimports", "negotiatedapiresources")
				clusterController, err := cluster.NewController(
					adminConfig,
					s.cfg.SyncerImage,
					*kubeconfig,
					s.cfg.ResourcesToSync,
					syncerMode,
				)
				if err != nil {
					return err
				}
				clusterController.Start(2)

				apiresourceController, err := apiresource.NewController(
					adminConfig,
					s.cfg.AutoPublishAPIs,
				)
				if err != nil {
					return err
				}
				apiresourceController.Start(2)

				return nil
			}); err != nil {
				return err
			}
		}

		prepared := server.PrepareRun()

		return prepared.Run(ctx.Done())
	}

	if len(s.cfg.EtcdClientInfo.Endpoints) == 0 {
		// No etcd servers specified so create one in-process:
		return es.Run(runFunc)
	}

	s.cfg.EtcdClientInfo.TLS = &tls.Config{
		InsecureSkipVerify: true,
	}

	if len(s.cfg.EtcdClientInfo.CertFile) > 0 && len(s.cfg.EtcdClientInfo.KeyFile) > 0 {
		cert, err := tls.LoadX509KeyPair(s.cfg.EtcdClientInfo.CertFile, s.cfg.EtcdClientInfo.KeyFile)
		if err != nil {
			return fmt.Errorf("failed to load x509 keypair: %s", err)
		}
		s.cfg.EtcdClientInfo.TLS.Certificates = []tls.Certificate{cert}
	}

	if len(s.cfg.EtcdClientInfo.TrustedCAFile) > 0 {
		if caCert, err := ioutil.ReadFile(s.cfg.EtcdClientInfo.TrustedCAFile); err != nil {
			return fmt.Errorf("failed to read ca file: %s", err)
		} else {
			caPool := x509.NewCertPool()
			caPool.AppendCertsFromPEM(caCert)
			s.cfg.EtcdClientInfo.TLS.RootCAs = caPool
			s.cfg.EtcdClientInfo.TLS.InsecureSkipVerify = false
		}
	}

	return runFunc(s.cfg.EtcdClientInfo)
}

// AddPostStartHook allows you to add a PostStartHook that gets passed to the underlying genericapiserver implementation.
func (s *Server) AddPostStartHook(name string, hook genericapiserver.PostStartHookFunc) {
	// you could potentially add duplicate or invalid post start hooks here, but we'll let
	// the genericapiserver implementation do its own validation during startup.
	s.postStartHooks = append(s.postStartHooks, postStartHookEntry{
		name: name,
		hook: hook,
	})
}

// AddPreShutdownHook allows you to add a PreShutdownHookFunc that gets passed to the underlying genericapiserver implementation.
func (s *Server) AddPreShutdownHook(name string, hook genericapiserver.PreShutdownHookFunc) {
	// you could potentially add duplicate or invalid post start hooks here, but we'll let
	// the genericapiserver implementation do its own validation during startup.
	s.preShutdownHooks = append(s.preShutdownHooks, preShutdownHookEntry{
		name: name,
		hook: hook,
	})
}

// NewServer creates a new instance of Server which manages the KCP api-server.
func NewServer(cfg *Config) *Server {
	return &Server{cfg: cfg}
}
