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
	"github.com/kcp-dev/kcp/pkg/virtual/generic/builders"
	virtualgenericcmd "github.com/kcp-dev/kcp/pkg/virtual/generic/cmd"
	virtualgenericrbac "github.com/kcp-dev/kcp/pkg/virtual/generic/rbac"
	virtualrootapiserver "github.com/kcp-dev/kcp/pkg/virtual/generic/rootapiserver"
	virtualworkspacesbuilders "github.com/kcp-dev/kcp/pkg/virtual/workspaces/builders"
)

var _ virtualgenericcmd.SubCommandOptions = (*WorkspacesSubCommandOptions)(nil)

type WorkspacesSubCommandOptions struct {
	RootPathPrefix string
	KubeconfigFile string
}

func (o *WorkspacesSubCommandOptions) Description() virtualgenericcmd.SubCommandDescription {
	return virtualgenericcmd.SubCommandDescription{
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

	flags.StringVar(&o.RootPathPrefix, "workspaces:root-path-prefix", virtualworkspacesbuilders.DefaultRootPathPrefix, ""+
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

func (o *WorkspacesSubCommandOptions) InitializeBuilders() ([]virtualrootapiserver.InformerStart, []builders.VirtualWorkspaceBuilder, error) {
	kubeConfig, err := virtualgenericcmd.ReadKubeConfig(o.KubeconfigFile)
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

	singleClusterRBACV1 := virtualgenericrbac.FilterPerCluster(adminLogicalClusterName, kubeInformers.Rbac().V1())

	subjectLocator := virtualgenericrbac.NewSubjectLocator(singleClusterRBACV1)
	ruleResolver := virtualgenericrbac.NewRuleResolver(singleClusterRBACV1)

	builders := []builders.VirtualWorkspaceBuilder{
		virtualworkspacesbuilders.WorkspacesVirtualWorkspaceBuilder(o.RootPathPrefix, kcpInformer.Tenancy().V1alpha1().Workspaces(), kcpClient.TenancyV1alpha1().Workspaces(), kubeClient, singleClusterRBACV1, subjectLocator, ruleResolver),
	}
	informerStarts := []virtualrootapiserver.InformerStart{
		kubeInformers.Start,
		kcpInformer.Start,
	}
	return informerStarts, builders, nil
}
