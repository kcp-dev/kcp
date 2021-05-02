package main

import (
	"flag"
	"io/ioutil"
	"log"

	"github.com/kcp-dev/kcp/pkg/reconciler/cluster"
	"k8s.io/client-go/tools/clientcmd"
)

const numThreads = 2

var (
	kubeconfig  = flag.String("kubeconfig", "", "Path to kubeconfig")
	syncerImage = flag.String("syncer_image", "", "Syncer image to install on clusters")
)

func main() {
	flag.Parse()

	r, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		log.Fatal(err)
	}

	kubeconfigBytes, err := ioutil.ReadFile(*kubeconfig)
	if err != nil {
		log.Fatal(err)
	}

	cluster.NewController(r, *syncerImage, string(kubeconfigBytes)).Start(numThreads)
}
