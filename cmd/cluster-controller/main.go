package main

import (
	"flag"
	"log"

	"github.com/kcp-dev/kcp/pkg/reconciler/cluster"
	"k8s.io/client-go/tools/clientcmd"
)

const numThreads = 2

var (
	kubeconfigPath  = flag.String("kubeconfig", "", "Path to kubeconfig")
	syncerImage = flag.String("syncer_image", "", "Syncer image to install on clusters")
)

func main() {
	flag.Parse()

	configLoader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: *kubeconfigPath},
		&clientcmd.ConfigOverrides{})

	r, err := configLoader.ClientConfig()
	if err != nil {
		log.Fatal(err)
	}

	kubeconfig, err := configLoader.RawConfig() 
	if err != nil {
		log.Fatal(err)
	}

	cluster.NewController(r, *syncerImage, kubeconfig).Start(numThreads)
}
