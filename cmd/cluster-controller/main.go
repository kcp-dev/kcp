package main

import (
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/kcp-dev/kcp/pkg/reconciler/apiresource"
	"github.com/kcp-dev/kcp/pkg/reconciler/cluster"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog"
	"k8s.io/kubernetes/pkg/controlplane/clientutils"
)

const numThreads = 2

var (
	kubeconfigPath  = flag.String("kubeconfig", "", "Path to kubeconfig")
	syncerImage     = flag.String("syncer_image", "", "Syncer image to install on clusters")
	pullMode        = flag.Bool("pull_mode", true, "Deploy the syncer in registered physical clusters in POD, and have it sync resources from KCP")
	pushMode        = flag.Bool("push_mode", false, "If true, run syncer for each cluster from inside cluster controller")
	autoPublishAPIs = flag.Bool("auto_publish_apis", false, "If true, the APIs imported from physical clusters will be published automatically as CRDs")
)

func main() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	flag.Parse()

	configLoader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: *kubeconfigPath},
		&clientcmd.ConfigOverrides{})

	r, err := configLoader.ClientConfig()
	if err != nil {
		klog.Fatal(err)
	}
	clientutils.EnableMultiCluster(r, nil, "clusters", "customresourcedefinitions", "apiresourceimports", "negotiatedapiresources")
	kubeconfig, err := configLoader.RawConfig()
	if err != nil {
		klog.Fatal(err)
	}

	resourcesToSync := flag.Args()
	if len(resourcesToSync) == 0 {
		resourcesToSync = []string{"pods", "deployments.apps"}
	}
	klog.Infof("Syncing resources: %v", resourcesToSync)

	if *pullMode && *pushMode {
		klog.Fatal("can't set --push_mode and --pull_mode")
	}
	syncerMode := cluster.SyncerModeNone
	if *pullMode {
		syncerMode = cluster.SyncerModePull
	}
	if *pushMode {
		syncerMode = cluster.SyncerModePush
	}

	clusterController := cluster.NewController(r, *syncerImage, kubeconfig, resourcesToSync, syncerMode)
	clusterController.Start(numThreads)

	apiresourceController := apiresource.NewController(
		r,
		*autoPublishAPIs,
	)
	apiresourceController.Start(2)

	go func() {
		<-sigs
		clusterController.Stop()
		apiresourceController.Stop()
	}()

	<-clusterController.Done()
	<-apiresourceController.Done()
}
