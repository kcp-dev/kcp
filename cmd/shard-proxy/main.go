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
	"errors"
	"os"
	"runtime"
	"time"

	"github.com/spf13/pflag"

	genericapiserver "k8s.io/apiserver/pkg/server"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpexternalversions "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/reconciler/workspaceindex"
)

const resyncPeriod = 10 * time.Hour

func defaultOptions() *options {
	return &options{
		numThreads: runtime.NumCPU(),
	}
}

func bindOptions(defaultOptions *options, fs *pflag.FlagSet) *options {
	fs.StringVar(&defaultOptions.rootKubeconfigPath, "root-kubeconfig", "", "Path to root kubeconfig.")
	fs.IntVar(&defaultOptions.numThreads, "threads", defaultOptions.numThreads, "Number of threads to use.")
	fs.IntVar(&defaultOptions.port, "port", defaultOptions.port, "Port to serve index on.")
	return defaultOptions
}

type options struct {
	// rootKubeconfigPath should hold a kubeconfig where the current context is
	// scoped to the logical cluster that contains organization workspaces
	rootKubeconfigPath string
	numThreads         int
	port               int
}

func (o *options) Validate() error {
	if o.rootKubeconfigPath == "" {
		return errors.New("--root-kubeconfig is required")
	}
	return nil
}

func main() {
	ctx := genericapiserver.SetupSignalContext()

	fs := pflag.NewFlagSet("shard-proxy", pflag.ContinueOnError)
	o := bindOptions(defaultOptions(), fs)
	if err := fs.Parse(os.Args[1:]); err != nil {
		klog.Fatalf("failed to parse arguments: %v", err)
	}
	if err := o.Validate(); err != nil {
		klog.Fatalf("invalid options: %v", err)
	}

	rootConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: o.rootKubeconfigPath}, nil).ClientConfig()
	if err != nil {
		klog.Fatalf("failed to load root credentials: %v", err)
	}
	rootClient, err := kcpclient.NewForConfig(rootConfig)
	if err != nil {
		klog.Fatalf("failed to create root client: %v", err)
	}
	kcpSharedInformerFactory := kcpexternalversions.NewSharedInformerFactoryWithOptions(rootClient, resyncPeriod)
	rootKubeClient, err := kubernetesclientset.NewClusterForConfig(rootConfig)
	if err != nil {
		klog.Fatalf("failed to create root k8s client: %v", err)
	}

	index := workspaceindex.NewIndex()
	controller, err := workspaceindex.NewController(
		rootKubeClient,
		kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces(),
		kcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceShards(),
		index,
	)
	if err != nil {
		klog.Fatalf("failed to create workspace index controller: %v", err)
	}
	server := workspaceindex.NewServer(o.port, kcpSharedInformerFactory, index, controller.Stable)

	kcpSharedInformerFactory.Start(ctx.Done())
	go controller.Start(ctx, o.numThreads)
	go server.ListenAndServe(ctx)

	<-ctx.Done()
}
