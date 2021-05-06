package main

import (
	"flag"
	"fmt"
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
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"
	"k8s.io/kube-openapi/pkg/util/sets"
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

	fromClient := dynamic.NewForConfigOrDie(fromConfig)
	fromDSIF := dynamicinformer.NewFilteredDynamicSharedInformerFactory(fromClient, resyncPeriod, metav1.NamespaceAll, func(o *metav1.ListOptions) {
		o.LabelSelector = fmt.Sprintf("cluster = %s", *clusterID)
	})

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

	// Create a client to modify "to".
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
	gvrstrs, err := getAllGVRs(fromConfig, syncedResourceTypes...)
	if err != nil {
		klog.Fatal(err)
	}
	for _, gvrstr := range gvrstrs {
		gvr, _ := schema.ParseResourceArg(gvrstr)

		if _, err := fromDSIF.ForResource(*gvr).Lister().List(labels.Everything()); err != nil {
			klog.Infof("Failed to list all %q: %v", gvrstr, err)
			continue
		}

		fromDSIF.ForResource(*gvr).Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.AddToQueue(*gvr, obj) },
			UpdateFunc: func(_, obj interface{}) { c.AddToQueue(*gvr, obj) },
			DeleteFunc: func(obj interface{}) { c.AddToQueue(*gvr, obj) },
		})
		klog.Infof("Set up informer for %v", gvr)
	}
	stopCh := make(chan struct{})
	fromDSIF.WaitForCacheSync(stopCh)
	fromDSIF.Start(stopCh)

	for i := 0; i < numThreads; i++ {
		go wait.Until(c.StartWorker, time.Second, stopCh)
	}
	klog.Infoln("Starting workers")
	<-stopCh
	klog.Infoln("Stopping workers")
}

func contains(ss []string, s string) bool {
	for _, n := range ss {
		if n == s {
			return true
		}
	}
	return false
}

func getAllGVRs(config *rest.Config, resourcesToSync ...string) ([]string, error) {
	toSyncSet := sets.NewString(resourcesToSync...)
	willBeSyncedSet := sets.NewString()
	dc, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, err
	}
	rs, err := dc.ServerPreferredResources()
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
			if !toSyncSet.Has(ai.Name) {
				// We're not interested in this resource type
				continue
			}
			if strings.Contains(ai.Name, "/") {
				// foo/status, pods/exec, namespace/finalize, etc.
				continue
			}
			if !ai.Namespaced {
				// Ignore cluster-scoped things.
				continue
			}
			if !contains(ai.Verbs, "watch") {
				klog.Infof("resource %s %s is not watchable: %v", vr, ai.Name, ai.Verbs)
				continue
			}
			gvrstrs = append(gvrstrs, fmt.Sprintf("%s.%s", ai.Name, vr))
			willBeSyncedSet.Insert(ai.Name)
		}
	}

	notFoundResourceTypes := toSyncSet.Difference(willBeSyncedSet)
	if notFoundResourceTypes.Len() != 0 {
		return nil, fmt.Errorf("The following resource types should be synced and we not found in the KCP logical cluster: %v", notFoundResourceTypes.List())
	}
	return gvrstrs, nil
}
