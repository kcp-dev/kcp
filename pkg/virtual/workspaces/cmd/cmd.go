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

package cmd

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	virtualframeworkcmd "github.com/kcp-dev/kcp/pkg/virtual/framework/cmd"
	frameworkrbac "github.com/kcp-dev/kcp/pkg/virtual/framework/rbac"
	rootapiserver "github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	"github.com/kcp-dev/kcp/pkg/virtual/workspaces/builder"
)

var _ virtualframeworkcmd.SubCommandOptions = (*WorkspacesSubCommandOptions)(nil)

type WorkspacesSubCommandOptions struct {
	RootPathPrefix string
	KubeconfigFile string
}

func (o *WorkspacesSubCommandOptions) Description() virtualframeworkcmd.SubCommandDescription {
	return virtualframeworkcmd.SubCommandDescription{
		Name:  "workspaces",
		Use:   "workspaces",
		Short: "Launch workspaces virtual workspace apiserver",
		Long:  "Start a virtual workspace apiserver to managing personal, organizational or global workspaces",
	}
}

func (o *WorkspacesSubCommandOptions) AddFlags(flags *pflag.FlagSet) {
	if o == nil {
		return
	}

	flags.StringVar(&o.KubeconfigFile, "workspaces:kubeconfig", "", ""+
		"The kubeconfig file of the organizational cluster that provides the workspaces and related RBAC rules.")

	_ = cobra.MarkFlagRequired(flags, "kubeconfig")

	flags.StringVar(&o.RootPathPrefix, "workspaces:root-path-prefix", builder.DefaultRootPathPrefix, ""+
		"The prefix of the workspaces API server root path.\n"+
		"The final workspaces API root path will be of the form:\n    <root-path-prefix>/workspaces/<org-name>/personal|all")
}

func (o *WorkspacesSubCommandOptions) Validate() []error {
	if o == nil {
		return nil
	}
	errs := []error{}

	if len(o.KubeconfigFile) == 0 {
		errs = append(errs, errors.New("--workspaces:kubeconfig is required for this command"))
	}

	if !strings.HasPrefix(o.RootPathPrefix, "/") {
		errs = append(errs, fmt.Errorf("--workspaces:root-path-prefix %v should start with /", o.RootPathPrefix))
	}

	return errs
}

func (o *WorkspacesSubCommandOptions) PrepareVirtualWorkspaces() ([]rootapiserver.InformerStart, []framework.VirtualWorkspace, error) {
	kubeConfig, err := virtualframeworkcmd.ReadKubeConfig(o.KubeconfigFile)
	if err != nil {
		return nil, nil, err
	}
	kubeClientConfig, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, nil, err
	}

	// HACK HACK HACK
	// for now we pass a /cluster/<org> config to the virtual workspace apiserver. We take the
	// org workspace name from the host, and then reset it to be a cluster-less config.
	// TODO(david): have a multi-org workspace auth cache.
	u, err := url.Parse(kubeClientConfig.Host)
	if err != nil {
		return nil, nil, err
	}
	segments := strings.Split(u.Path, "/")
	orgClusterName := segments[len(segments)-1]
	u.Path = ""
	kubeClientConfig.Host = u.String()

	kubeClusterClient, err := kubernetes.NewClusterForConfig(kubeClientConfig)
	if err != nil {
		return nil, nil, err
	}
	orgKubeClient := kubeClusterClient.Cluster(orgClusterName)
	kubeInformers := informers.NewSharedInformerFactory(orgKubeClient, 10*time.Minute)
	rootKubeClient := kubeClusterClient.Cluster(helper.RootCluster)

	kcpClusterClient, err := kcpclient.NewClusterForConfig(kubeClientConfig)
	if err != nil {
		return nil, nil, err
	}
	orgKcpClient := kcpClusterClient.Cluster(orgClusterName)
	kcpInformer := kcpinformer.NewSharedInformerFactory(orgKcpClient, 10*time.Minute)
	rootKcpClient := kcpClusterClient.Cluster(helper.RootCluster)

	//	discoveryClient := cacheddiscovery.NewMemCacheClient(orgKubeClient.Discovery())
	//	restMapper := restmapper.NewDeferredDiscoveryRESTMapper(discoveryClient)

	// TODO(david): support multiple orgs
	singleClusterRBACV1 := frameworkrbac.FilterPerCluster(orgClusterName, kubeInformers.Rbac().V1())

	subjectLocator := frameworkrbac.NewSubjectLocator(singleClusterRBACV1)
	ruleResolver := frameworkrbac.NewRuleResolver(singleClusterRBACV1)

	virtualWorkspaces := []framework.VirtualWorkspace{
		builder.BuildVirtualWorkspace(o.RootPathPrefix, kcpInformer.Tenancy().V1alpha1().ClusterWorkspaces(), rootKcpClient, orgKcpClient, rootKubeClient, orgKubeClient, singleClusterRBACV1, subjectLocator, ruleResolver),
	}
	informerStarts := []rootapiserver.InformerStart{
		kubeInformers.Start,
		kcpInformer.Start,
	}
	return informerStarts, virtualWorkspaces, nil
}
