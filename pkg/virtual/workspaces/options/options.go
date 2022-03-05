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

package options

import (
	"fmt"
	"io/ioutil"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	"github.com/kcp-dev/kcp/pkg/virtual/workspaces/builder"
)

const DefaultRootPathPrefix string = "/services/workspaces"

type Workspaces struct {
	RootPathPrefix string
	KubeconfigFile string
}

func NewWorkspaces() *Workspaces {
	return &Workspaces{
		RootPathPrefix: DefaultRootPathPrefix,
	}
}

func (o *Workspaces) AddGenericFlags(flags *pflag.FlagSet, prefix string) {
	if o == nil {
		return
	}

	flags.StringVar(&o.RootPathPrefix, prefix+"workspaces-base-path", o.RootPathPrefix, ""+
		"The prefix of the workspaces API server root path.\n"+
		"The final workspaces API root path will be of the form:\n    <root-path-prefix>/<org-name>/personal|all")
}

func (o *Workspaces) AddStandaloneFlags(flags *pflag.FlagSet, prefix string) {
	if o == nil {
		return
	}

	flags.StringVar(&o.KubeconfigFile, "workspaces:kubeconfig", "", ""+
		"The kubeconfig file of the KCP instance that hosts workspaces.")

	_ = cobra.MarkFlagRequired(flags, "kubeconfig")
}

func (o *Workspaces) Validate(prefix string) []error {
	if o == nil {
		return nil
	}
	errs := []error{}

	if len(o.KubeconfigFile) == 0 {
		errs = append(errs, fmt.Errorf("--%s-workspaces:kubeconfig is required for this command", prefix))
	}

	if !strings.HasPrefix(o.RootPathPrefix, "/") {
		errs = append(errs, fmt.Errorf("--%s-workspaces-base-path %v should start with /", prefix, o.RootPathPrefix))
	}

	return errs
}

func (o *Workspaces) NewVirtualWorkspaces() ([]rootapiserver.InformerStart, []framework.VirtualWorkspace, error) {
	kubeConfig, err := readKubeConfig(o.KubeconfigFile)
	if err != nil {
		return nil, nil, err
	}
	kubeClientConfig, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, nil, err
	}

	u, err := url.Parse(kubeClientConfig.Host)
	if err != nil {
		return nil, nil, err
	}
	u.Path = ""
	kubeClientConfig.Host = u.String()

	kubeClusterClient, err := kubernetes.NewClusterForConfig(kubeClientConfig)
	if err != nil {
		return nil, nil, err
	}
	wildcardKubeClient := kubeClusterClient.Cluster("*")
	wildcardKubeInformers := informers.NewSharedInformerFactory(wildcardKubeClient, 10*time.Minute)
	rootKubeClient := kubeClusterClient.Cluster(helper.RootCluster)

	kcpClusterClient, err := kcpclient.NewClusterForConfig(kubeClientConfig)
	if err != nil {
		return nil, nil, err
	}
	wildcardKcpClient := kcpClusterClient.Cluster("*")
	wildcardKcpInformers := kcpinformer.NewSharedInformerFactory(wildcardKcpClient, 10*time.Minute)
	rootKcpClient := kcpClusterClient.Cluster(helper.RootCluster)

	virtualWorkspaces := []framework.VirtualWorkspace{
		builder.BuildVirtualWorkspace(o.RootPathPrefix, wildcardKcpInformers.Tenancy().V1alpha1().ClusterWorkspaces(), wildcardKubeInformers.Rbac().V1(), rootKcpClient, rootKubeClient, kcpClusterClient, kubeClusterClient),
	}
	informerStarts := []rootapiserver.InformerStart{
		wildcardKubeInformers.Start,
		wildcardKcpInformers.Start,
	}
	return informerStarts, virtualWorkspaces, nil
}

func readKubeConfig(kubeConfigFile string) (clientcmd.ClientConfig, error) {
	// Resolve relative to CWD
	absoluteKubeConfigFile, err := api.MakeAbs(kubeConfigFile, "")
	if err != nil {
		return nil, err
	}

	kubeConfigBytes, err := ioutil.ReadFile(absoluteKubeConfigFile)
	if err != nil {
		return nil, err
	}
	kubeConfig, err := clientcmd.NewClientConfigFromBytes(kubeConfigBytes)
	if err != nil {
		return nil, err
	}
	return kubeConfig, nil
}
