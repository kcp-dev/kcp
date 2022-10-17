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
	"time"

	"github.com/kcp-dev/logicalcluster/v2"

	kcpclienthelper "github.com/kcp-dev/apimachinery/pkg/client"
	kubernetesinformers "github.com/kcp-dev/client-go/informers"
	kubernetesclient "github.com/kcp-dev/client-go/kubernetes"
	"k8s.io/client-go/pkg/version"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/kcp/pkg/reconciler/coordination/deployment"
)

const numThreads = 2

const resyncPeriod = 10 * time.Hour

var kubeconfig = flag.String("kubeconfig", "", "Path to kubeconfig")
var kubecontext = flag.String("context", "", "Context to use in the Kubeconfig file, instead of the current context")
var workspaceName = flag.String("workspace", "", "KCP workspace to look into")

func main() {
	flag.Parse()

	var overrides clientcmd.ConfigOverrides
	if *kubecontext != "" {
		overrides.CurrentContext = *kubecontext
	}

	r, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: *kubeconfig},
		&overrides).ClientConfig()
	if err != nil {
		panic(err)
	}

	kcpVersion := version.Get().GitVersion

	kcpCluterClient, err := kubernetesclient.NewForConfig(kcpclienthelper.SetMultiClusterRoundTripper(rest.AddUserAgent(rest.CopyConfig(r), "kcp#syncer/"+kcpVersion)))
	if err != nil {
		panic(err)
	}

	kubeInformerFactory := kubernetesinformers.NewSharedInformerFactoryWithOptions(kcpCluterClient, resyncPeriod)

	var informer cache.SharedIndexInformer
	if workspaceName == nil || *workspaceName == "" {
		informer = kubeInformerFactory.Apps().V1().Deployments().Informer()
	} else {
		informer = kubeInformerFactory.Apps().V1().Deployments().Cluster(logicalcluster.New(*workspaceName)).Informer()
	}

	ctx := context.Background()

	controller, err := deployment.NewController(ctx, kcpCluterClient, informer, kubeInformerFactory.Apps().V1().Deployments().Lister())
	if err != nil {
		panic(err)
	}
	kubeInformerFactory.Start(ctx.Done())
	kubeInformerFactory.WaitForCacheSync(ctx.Done())

	controller.Start(ctx, numThreads)
}
