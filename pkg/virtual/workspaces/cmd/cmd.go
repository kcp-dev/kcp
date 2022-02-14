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
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"

	tenancyAPI "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
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
		"The final workspaces API root path will be of the form:\n    <root-path-prefix>/personal|organization|global")
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

	apiExtensionsClient, err := apiextensionsclient.NewForConfig(kubeClientConfig)
	if err != nil {
		return nil, nil, err
	}

	workspacesCRD, err := apiExtensionsClient.ApiextensionsV1().CustomResourceDefinitions().Get(context.TODO(), "workspaces."+tenancyAPI.SchemeGroupVersion.Group, metav1.GetOptions{})
	if kerrors.IsNotFound(err) {
		return nil, nil, errors.New("The Workspaces CRD should be registered in the cluster providing the workspaces")
	}
	if err != nil {
		return nil, nil, err
	}

	adminLogicalClusterName := workspacesCRD.ClusterName

	kubeClientClusterChooser, err := kubernetes.NewClusterForConfig(kubeClientConfig)
	if err != nil {
		return nil, nil, err
	}
	kubeClient := kubeClientClusterChooser.Cluster(adminLogicalClusterName)

	kubeInformers := informers.NewSharedInformerFactory(kubeClient, 10*time.Minute)

	kcpClientClusterChooser, err := kcpclient.NewClusterForConfig(kubeClientConfig)
	if err != nil {
		return nil, nil, err
	}
	kcpClient := kcpClientClusterChooser.Cluster(adminLogicalClusterName)

	kcpInformer := kcpinformer.NewSharedInformerFactory(kcpClient, 10*time.Minute)

	//	discoveryClient := cacheddiscovery.NewMemCacheClient(kubeClient.Discovery())
	//	restMapper := restmapper.NewDeferredDiscoveryRESTMapper(discoveryClient)

	singleClusterRBACV1 := frameworkrbac.FilterPerCluster(adminLogicalClusterName, kubeInformers.Rbac().V1())

	subjectLocator := frameworkrbac.NewSubjectLocator(singleClusterRBACV1)
	ruleResolver := frameworkrbac.NewRuleResolver(singleClusterRBACV1)

	virtualWorkspaces := []framework.VirtualWorkspace{
		builder.BuildVirtualWorkspace(o.RootPathPrefix, kcpInformer.Tenancy().V1alpha1().ClusterWorkspaces(), kcpClient, kubeClient, singleClusterRBACV1, subjectLocator, ruleResolver),
	}
	informerStarts := []rootapiserver.InformerStart{
		kubeInformers.Start,
		kcpInformer.Start,
	}
	return informerStarts, virtualWorkspaces, nil
}
