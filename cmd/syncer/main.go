package main

import (
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/kcp-dev/kcp/pkg/syncer"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

const (
	resyncPeriod = time.Hour
	numThreads   = 2
)

var (
	kubeconfig = flag.String("kubeconfig", "", "Config file for -from cluster")
	clusterID  = flag.String("cluster", "", "ID of this cluster")
)

func main() {
	flag.Parse()

	// Create a client to dynamically watch "from".
	//fromConfig, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	fromConfig, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal(err)
	}
	fromClient := dynamic.NewForConfigOrDie(fromConfig)
	fromDSIF := dynamicinformer.NewFilteredDynamicSharedInformerFactory(fromClient, resyncPeriod, metav1.NamespaceAll, func(o *metav1.ListOptions) {
		o.LabelSelector = fmt.Sprintf("cluster = %s", *clusterID)
	})

	// Create a client to modify "to".
	toConfig, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal(err)
	}
	toClient := dynamic.NewForConfigOrDie(toConfig)

	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	defer queue.ShutDown()

	c := syncer.Controller{
		// TODO: should we have separate upstream and downstream sync workqueues?
		Queue: queue,

		FromDSIF: fromDSIF,
		ToClient: toClient,
	}

	// Get all types the upstream API server knows about.
	// TODO: watch this and learn about new types, or forget about old ones.
	/*
		gvrstrs, err := getAllGVRs(fromConfig)
		if err != nil {
			log.Fatal(err)
		}
	*/
	// TODO: For now, only care about:
	gvrstrs := []string{
		".v1.namespaces",
		".v1.pods",
		".v1.deployments",
	}
	for _, gvrstr := range gvrstrs {
		gvr, _ := schema.ParseResourceArg(gvrstr)

		if _, err := fromDSIF.ForResource(*gvr).Lister().List(labels.Everything()); err != nil {
			log.Println("Failed to list all %q: %v", gvrstr, err)
			continue
		}

		fromDSIF.ForResource(*gvr).Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.AddToQueue(*gvr, obj) },
			UpdateFunc: func(_, obj interface{}) { c.AddToQueue(*gvr, obj) },
			DeleteFunc: func(obj interface{}) { c.AddToQueue(*gvr, obj) },
		})
		log.Printf("Set up informer for %v", gvr)
	}
	stopCh := make(chan struct{})
	fromDSIF.WaitForCacheSync(stopCh)
	fromDSIF.Start(stopCh)

	for i := 0; i < numThreads; i++ {
		go wait.Until(c.StartWorker, time.Second, stopCh)
	}
	log.Println("Starting workers")
	<-stopCh
	log.Println("Stopping workers")
}

func contains(ss []string, s string) bool {
	for _, n := range ss {
		if n == s {
			return true
		}
	}
	return false
}

func getAllGVRs(config *rest.Config) ([]string, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, err
	}
	rs, err := dc.ServerResources()
	if err != nil {
		return nil, err
	}
	var gvrstrs []string
	for _, r := range rs {
		// v1 -> v1.
		// apps/v1 -> v1.apps
		// tekton.dev/v1beta1 -> v1beta1.tekton.dev
		parts := strings.SplitN(r.GroupVersion, "/", 2)
		vr := parts[0] + "."
		if len(parts) == 2 {
			vr = parts[1] + "." + parts[0]
		}
		for _, ai := range r.APIResources {
			if strings.Contains(ai.Name, "/") {
				// foo/status, pods/exec, namespace/finalize, etc.
				continue
			}
			if !ai.Namespaced {
				// Ignore cluster-scoped things.
				continue
			}
			if !contains(ai.Verbs, "watch") {
				log.Printf("resource %s %s is not watchable: %v", vr, ai.Name, ai.Verbs)
				continue
			}
			gvrstrs = append(gvrstrs, fmt.Sprintf("%s.%s", ai.Name, vr))
		}
	}
	return gvrstrs, nil
}
