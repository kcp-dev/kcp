package main

import (
	"flag"
	"time"

	"github.com/kcp-dev/kcp/pkg/syncer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog"
)

const (
	resyncPeriod = time.Hour
	numThreads   = 2
)

var (
	fromKubeconfig = flag.String("from_kubeconfig", "", "Kubeconfig file for -from cluster")
	fromContext    = flag.String("from_context", "", "Context to use in the Kubeconfig file for -from cluster, instead of the current context")
	toKubeconfig   = flag.String("to_kubeconfig", "", "Kubeconfig file for -to cluster. If not set, the InCluster configuration will be used")
	toContext      = flag.String("to_context", "", "Context to use in the Kubeconfig file for -to cluster, instead of the current context")
	clusterID      = flag.String("cluster", "", "ID of this cluster")
)

func main() {
	flag.Parse()
	syncedResourceTypes := flag.Args()
	if len(syncedResourceTypes) == 0 {
		syncedResourceTypes = []string{"pods", "deployments"}
	}

	// Create a client to dynamically watch "from".

	var fromOverrides clientcmd.ConfigOverrides
	if *fromContext != "" {
		fromOverrides.CurrentContext = *fromContext
	}

	fromConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: *fromKubeconfig},
		&fromOverrides).ClientConfig()
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

	s, err := syncer.New(fromConfig, toConfig, syncedResourceTypes, *clusterID)
	if err != nil {
		klog.Fatal(err)
	}

	s.Start(numThreads)
	klog.Infoln("Starting workers")
	<-s.Done()
	klog.Infoln("Stopping workers")
}
