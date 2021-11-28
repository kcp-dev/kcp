package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	v1 "k8s.io/api/core/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsv1client "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	crdexternalversions "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/storage/storagebackend"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/kubernetes/pkg/controller/namespace"
	"k8s.io/kubernetes/pkg/genericcontrolplane"
	"k8s.io/kubernetes/pkg/genericcontrolplane/clientutils"
	"k8s.io/kubernetes/pkg/genericcontrolplane/options"

	"github.com/kcp-dev/kcp/config"
	apiresourceapi "github.com/kcp-dev/kcp/pkg/apis/apiresource"
	clusterapi "github.com/kcp-dev/kcp/pkg/apis/cluster"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpexternalversions "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/etcd"
	"github.com/kcp-dev/kcp/pkg/reconciler/apiresource"
	"github.com/kcp-dev/kcp/pkg/reconciler/cluster"
	"github.com/kcp-dev/kcp/pkg/sharding"
)

const resyncPeriod = 10 * time.Hour

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
	if s.cfg.ProfilerAddress != "" {
		// nolint:errcheck
		go http.ListenAndServe(s.cfg.ProfilerAddress, nil)
	}

	var dir string
	if filepath.IsAbs(s.cfg.RootDirectory) {
		dir = s.cfg.RootDirectory
	} else {
		dir = filepath.Join(".", s.cfg.RootDirectory)
	}
	if len(dir) != 0 {
		if fi, err := os.Stat(dir); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
			if err := os.MkdirAll(dir, 0755); err != nil {
				return err
			}
		} else {
			if !fi.IsDir() {
				return fmt.Errorf("%q is a file, please delete or select another location", dir)
			}
		}
	}

	etcdDir := filepath.Join(dir, s.cfg.EtcdDirectory)

	if len(s.cfg.EtcdClientInfo.Endpoints) == 0 {
		// No etcd servers specified so create one in-process:
		es := &etcd.Server{
			Dir: etcdDir,
		}
		embeddedClientInfo, err := es.Run(ctx, s.cfg.EtcdPeerPort, s.cfg.EtcdClientPort)
		if err != nil {
			return err
		}
		s.cfg.EtcdClientInfo = embeddedClientInfo
	} else {
		// Set up for connection to an external etcd cluster
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
	}

	c, err := clientv3.New(clientv3.Config{
		Endpoints: s.cfg.EtcdClientInfo.Endpoints,
		TLS:       s.cfg.EtcdClientInfo.TLS,
	})
	if err != nil {
		return err
	}
	defer c.Close()
	r, err := c.Cluster.MemberList(ctx)
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

	injector := make(chan sharding.IdentifiedConfig)
	clientLoader, err := sharding.New(s.cfg.ShardKubeconfigFile, injector)
	if err != nil {
		return err
	}
	serverOptions.BuildHandlerChainFunc = func(apiHandler http.Handler, c *genericapiserver.Config) (secure http.Handler) {
		// we want a request to hit the chain like:
		// - lcluster handler (this package's ServeHTTP)
		// - shard proxy (sharding.ServeHTTP)
		// - original handler chain
		// the lcluster handler is a pass-through, not a delegate, so the wrapping looks weird
		if s.cfg.EnableSharding {
			apiHandler = http.HandlerFunc(sharding.ServeHTTP(apiHandler, clientLoader))
		}
		apiHandler = http.HandlerFunc(ServeHTTP(genericapiserver.DefaultBuildHandlerChain(apiHandler, c), c))

		return apiHandler
	}

	serverOptions.SecureServing.ServerCert.CertDirectory = etcdDir
	serverOptions.Etcd.StorageConfig.Transport = storagebackend.TransportConfig{
		ServerList:    s.cfg.EtcdClientInfo.Endpoints,
		CertFile:      s.cfg.EtcdClientInfo.CertFile,
		KeyFile:       s.cfg.EtcdClientInfo.KeyFile,
		TrustedCAFile: s.cfg.EtcdClientInfo.TrustedCAFile,
	}
	serverOptions.Authentication.ClientCert.ClientCA = s.cfg.ClientCAFile
	cpOptions, err := genericcontrolplane.Complete(serverOptions)
	if err != nil {
		return err
	}

	server, err := genericcontrolplane.CreateServerChain(cpOptions, ctx.Done())
	if err != nil {
		return err
	}

	//Create Client and Shared
	var clientConfig clientcmdapi.Config
	clientConfig.AuthInfos = map[string]*clientcmdapi.AuthInfo{
		"loopback": {Token: server.LoopbackClientConfig.BearerToken},
	}
	clientConfig.Clusters = map[string]*clientcmdapi.Cluster{
		// cross-cluster is the virtual cluster running by default
		"cross-cluster": {
			Server:                   server.LoopbackClientConfig.Host + "/clusters/*",
			CertificateAuthorityData: server.LoopbackClientConfig.CAData,
			TLSServerName:            server.LoopbackClientConfig.TLSClientConfig.ServerName,
		},
		// admin is the virtual cluster running by default
		"admin": {
			Server:                   server.LoopbackClientConfig.Host,
			CertificateAuthorityData: server.LoopbackClientConfig.CAData,
			TLSServerName:            server.LoopbackClientConfig.TLSClientConfig.ServerName,
		},
		// user is a virtual cluster that is lazily instantiated
		"user": {
			Server:                   server.LoopbackClientConfig.Host + "/clusters/" + genericcontrolplane.SanitizedClusterName(server.ExternalAddress, "user"),
			CertificateAuthorityData: server.LoopbackClientConfig.CAData,
			TLSServerName:            server.LoopbackClientConfig.TLSClientConfig.ServerName,
		},
	}
	clientConfig.Contexts = map[string]*clientcmdapi.Context{
		"cross-cluster": {Cluster: "cross-cluster", AuthInfo: "loopback"},
		"admin":         {Cluster: "admin", AuthInfo: "loopback"},
		"user":          {Cluster: "user", AuthInfo: "loopback"},
	}
	clientConfig.CurrentContext = "admin"

	if s.cfg.EnableSharding {
		adminConfig, err := clientcmd.NewNonInteractiveClientConfig(clientConfig, "admin", &clientcmd.ConfigOverrides{}, nil).ClientConfig()
		if err != nil {
			return err
		}
		injector <- sharding.IdentifiedConfig{
			Identifier: server.ExternalAddress,
			Config:     adminConfig,
		}

		// we need to broadcast our name to others, and today we're doing that with context names becase we have no good way to
		// know which of these resolved names ends up being portable ... some are ipv6 loopback addresses. some are v4 ... in general
		// we may need to take our name as input or use DNS?
		id := genericcontrolplane.SanitizeClusterId(server.ExternalAddress)
		if err := clientcmd.WriteToFile(clientcmdapi.Config{
			AuthInfos: map[string]*clientcmdapi.AuthInfo{
				"loopback": {Token: server.LoopbackClientConfig.BearerToken},
			},
			Clusters: map[string]*clientcmdapi.Cluster{
				id: {
					Server:                   server.LoopbackClientConfig.Host,
					CertificateAuthorityData: server.LoopbackClientConfig.CAData,
					TLSServerName:            server.LoopbackClientConfig.TLSClientConfig.ServerName,
				},
			},
			Contexts: map[string]*clientcmdapi.Context{
				id: {Cluster: id, AuthInfo: "loopback"},
			},
			CurrentContext: id,
		}, filepath.Join(s.cfg.RootDirectory, "data", "shard.kubeconfig")); err != nil {
			return err
		}
	}
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
		adminConfig, err := clientcmd.NewNonInteractiveClientConfig(clientConfig, "admin", &clientcmd.ConfigOverrides{}, nil).ClientConfig()
		if err != nil {
			return err
		}

		kcpSharedInformerFactory := kcpexternalversions.NewSharedInformerFactoryWithOptions(kcpclient.NewForConfigOrDie(adminConfig), resyncPeriod)
		crdSharedInformerFactory := crdexternalversions.NewSharedInformerFactoryWithOptions(apiextensionsclient.NewForConfigOrDie(adminConfig), resyncPeriod)

		if err := server.AddPostStartHook("Install Cluster Controller", func(context genericapiserver.PostStartHookContext) error {
			// Register CRDs in both the admin and user logical clusters
			requiredCrds := []metav1.GroupKind{
				{Group: apiresourceapi.GroupName, Kind: "apiresourceimports"},
				{Group: apiresourceapi.GroupName, Kind: "negotiatedapiresources"},
				{Group: clusterapi.GroupName, Kind: "clusters"},
			}
			for _, contextName := range []string{"admin", "user"} {
				logicalClusterConfig, err := clientcmd.NewNonInteractiveClientConfig(clientConfig, contextName, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
				if err != nil {
					return err
				}
				crdClient := apiextensionsv1client.NewForConfigOrDie(logicalClusterConfig).CustomResourceDefinitions()
				if err := config.BootstrapCustomResourceDefinitions(ctx, crdClient, requiredCrds); err != nil {
					return err
				}
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

			clientutils.EnableMultiCluster(adminConfig, nil, true, "clusters", "customresourcedefinitions", "apiresourceimports", "negotiatedapiresources")

			apiExtensionsClient := apiextensionsclient.NewForConfigOrDie(adminConfig)
			kcpClient := kcpclient.NewForConfigOrDie(adminConfig)

			clusterController, err := cluster.NewController(
				apiExtensionsClient,
				kcpClient,
				kcpSharedInformerFactory.Cluster().V1alpha1().Clusters(),
				kcpSharedInformerFactory.Apiresource().V1alpha1().APIResourceImports(),
				s.cfg.SyncerImage,
				*kubeconfig,
				s.cfg.ResourcesToSync,
				syncerMode,
			)
			if err != nil {
				return err
			}

			apiresourceController, err := apiresource.NewController(
				apiExtensionsClient,
				kcpClient,
				s.cfg.AutoPublishAPIs,
				kcpSharedInformerFactory.Apiresource().V1alpha1().NegotiatedAPIResources(),
				kcpSharedInformerFactory.Apiresource().V1alpha1().APIResourceImports(),
				crdSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions(),
			)
			if err != nil {
				return err
			}

			kcpSharedInformerFactory.Start(context.StopCh)
			crdSharedInformerFactory.Start(context.StopCh)
			kcpSharedInformerFactory.WaitForCacheSync(context.StopCh)
			crdSharedInformerFactory.WaitForCacheSync(context.StopCh)

			go clusterController.Start(ctx, 2)
			go apiresourceController.Start(ctx, 2)

			return nil
		}); err != nil {
			return err
		}
	}

	prepared := server.PrepareRun()

	return prepared.Run(ctx.Done())
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
	s := &Server{cfg: cfg}
	//During the creation of the server, we know that we want to add the namespace controller.
	s.postStartHooks = append(s.postStartHooks, postStartHookEntry{
		name: "start-namespace-controller",
		hook: s.startNamespaceController,
	})
	return s
}

func (s *Server) startNamespaceController(hookContext genericapiserver.PostStartHookContext) error {
	kubeClient, err := kubernetes.NewForConfig(hookContext.LoopbackClientConfig)
	if err != nil {
		return err
	}
	metadata, err := metadata.NewForConfig(hookContext.LoopbackClientConfig)
	if err != nil {
		return err
	}
	versionedInformer := informers.NewSharedInformerFactory(kubeClient, resyncPeriod)

	discoverResourcesFn := func(clusterName string) ([]*metav1.APIResourceList, error) {
		logicalClusterConfig := rest.CopyConfig(hookContext.LoopbackClientConfig)
		logicalClusterConfig.Host += "/clusters/" + clusterName
		discoveryClient, err := discovery.NewDiscoveryClientForConfig(logicalClusterConfig)
		if err != nil {
			return nil, err
		}
		return discoveryClient.ServerPreferredNamespacedResources()
	}

	go namespace.NewNamespaceController(
		kubeClient,
		metadata,
		discoverResourcesFn,
		versionedInformer.Core().V1().Namespaces(),
		time.Duration(30)*time.Second,
		v1.FinalizerKubernetes,
	).Run(2, hookContext.StopCh)

	versionedInformer.Start(hookContext.StopCh)

	return nil
}
