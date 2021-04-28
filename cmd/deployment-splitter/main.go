package main

import (
	"flag"
	"log"

	"github.com/kcp-dev/kcp/pkg/reconciler/deployment"
	"k8s.io/client-go/tools/clientcmd"
)

const numThreads = 2

var kubeconfig = flag.String("kubeconfig", "", "Path to kubeconfig")

func main() {
	flag.Parse()

	r, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		log.Fatal(err)
	}

	deployment.NewController(r).Start(numThreads)
}
