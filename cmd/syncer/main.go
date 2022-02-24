/*
Copyright 2021 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path"
	"syscall"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/syncer"
)

const numThreads = 2

var (
	fromKubeconfig  = flag.String("from_kubeconfig", "", "Kubeconfig file for -from cluster.")
	fromClusterName = flag.String("from_cluster", "", "Name of the -from logical cluster.")
	toKubeconfig    = flag.String("to_kubeconfig", "", "Kubeconfig file for -to cluster. If not set, the InCluster configuration will be used.")
	toContext       = flag.String("to_context", "", "Context to use in the Kubeconfig file for -to cluster, instead of the current context.")
	pclusterID      = flag.String("cluster", "", "ID of the -to cluster. Resources with this ID set in the 'kcp.dev/cluster' label will be synced.")
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
	if *fromClusterName == "" {
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

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGILL, syscall.SIGINT)
	defer cancel()

	klog.Infoln("Starting workers")
	if err := syncer.StartSyncer(ctx, fromConfig, toConfig, sets.NewString(syncedResourceTypes...), *fromClusterName, *pclusterID, numThreads); err != nil {
		klog.Fatal(err)
	}

	<-ctx.Done()

	klog.Infoln("Stopping workers")
}
