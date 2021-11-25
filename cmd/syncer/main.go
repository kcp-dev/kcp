package main

import (
	"flag"
	"fmt"
	"os"
	"path"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/syncer"
)

const numThreads = 2

var (
	fromKubeconfig = flag.String("from_kubeconfig", "", "Kubeconfig file for -from cluster.")
	fromCluster    = flag.String("from_cluster", "", "Name of the -from logical cluster.")
	toKubeconfig   = flag.String("to_kubeconfig", "", "Kubeconfig file for -to cluster. If not set, the InCluster configuration will be used.")
	toContext      = flag.String("to_context", "", "Context to use in the Kubeconfig file for -to cluster, instead of the current context.")
	clusterID      = flag.String("cluster", "", "ID of the -to cluster. Resources with this ID set in the 'kcp.dev/cluster' label will be synced.")
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s [resourceTypes...]:\n", path.Base(os.Args[0]))
		flag.PrintDefaults()
	}
	flag.Parse()
	syncedResourceTypes := flag.Args()
	if len(syncedResourceTypes) == 0 {
		syncedResourceTypes = []string{"deployments.apps"}
		klog.Infoln("No resource types provided; using defaults.")
	}
	klog.Infof("Syncing the following resource types: %s", syncedResourceTypes)

	// Create a client to dynamically watch "from".
	if *fromCluster == "" {
		klog.Fatal("--from_cluster is required")
	}

	fromConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: *fromKubeconfig}, nil).ClientConfig()
	if err != nil {
		klog.Fatal(err)
	}

	var toConfig *rest.Config
	if *toKubeconfig != "" {
		var toOverrides clientcmd.ConfigOverrides
		if *toContext != "" {
			toOverrides.CurrentContext = *toContext
		}

		toConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: *toKubeconfig},
			&toOverrides).ClientConfig()
		if err != nil {
			klog.Fatal(err)
		}
	} else {
		toConfig, err = rest.InClusterConfig()
	}
	if err != nil {
		klog.Fatal(err)
	}

	syncer, err := syncer.StartSyncer(fromConfig, toConfig, sets.NewString(syncedResourceTypes...), *clusterID, *fromCluster, numThreads)
	if err != nil {
		klog.Fatal(err)
	}
	klog.Infoln("Starting workers")

	syncer.WaitUntilDone()
	klog.Infoln("Stopping workers")
}
