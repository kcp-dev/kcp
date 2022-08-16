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

package command

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net/url"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/spf13/cobra"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/wait"
	genericapiserver "k8s.io/apiserver/pkg/server"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/pkg/version"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/component-base/config"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	boostrap "github.com/kcp-dev/kcp/pkg/server/bootstrap"
	virtualrootapiserver "github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
)

func NewCommand(ctx context.Context, errout io.Writer) *cobra.Command {
	opts := options.NewOptions()

	// Default to -v=2
	opts.Logs.Config.Verbosity = config.VerbosityLevel(2)

	cmd := &cobra.Command{
		Use:   "workspaces",
		Short: "Launch workspaces virtual workspace apiserver",
		Long:  "Start a virtual workspace apiserver to managing personal, shared or organization workspaces",

		RunE: func(c *cobra.Command, args []string) error {
			if err := opts.Logs.ValidateAndApply(kcpfeatures.DefaultFeatureGate); err != nil {
				return err
			}
			if err := opts.Validate(); err != nil {
				return err
			}
			return Run(ctx, opts)
		},
	}

	opts.AddFlags(cmd.Flags())

	return cmd
}

// Run takes the options, starts the API server and waits until stopCh is closed or initial listening fails.
func Run(ctx context.Context, o *options.Options) error {
	// parse kubeconfig
	kubeConfig, err := readKubeConfig(o.KubeconfigFile)
	if err != nil {
		return err
	}
	nonIdentityConfig, err := kubeConfig.ClientConfig()
	if err != nil {
		return err
	}
	u, err := url.Parse(nonIdentityConfig.Host)
	if err != nil {
		return err
	}
	u.Path = ""
	nonIdentityConfig.Host = u.String()

	// resolve identities for system APIBindings
	nonIdentityKcpClusterClient, err := kcpclient.NewClusterForConfig(nonIdentityConfig) // can only used for wildcard requests of apis.kcp.dev
	if err != nil {
		return err
	}
	identityConfig, resolveIdentities := boostrap.NewConfigWithWildcardIdentities(nonIdentityConfig, boostrap.KcpRootGroupExportNames, boostrap.KcpRootGroupResourceExportNames, nonIdentityKcpClusterClient, nil)
	if err := wait.PollImmediateInfiniteWithContext(ctx, time.Millisecond*500, func(ctx context.Context) (bool, error) {
		if err := resolveIdentities(ctx); err != nil {
			klog.V(3).Infof("failed to resolve identities, keeping trying: %v", err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		return fmt.Errorf("failed to get or create identities: %w", err)
	}

	// create clients and informers
	kubeClusterClient, err := kubernetes.NewClusterForConfig(identityConfig)
	if err != nil {
		return err
	}

	wildcardKubeClient := kubeClusterClient.Cluster(logicalcluster.Wildcard)
	wildcardKubeInformers := kubeinformers.NewSharedInformerFactory(wildcardKubeClient, 10*time.Minute)

	kcpClusterClient, err := kcpclient.NewClusterForConfig(identityConfig)
	if err != nil {
		return err
	}
	wildcardKcpClient := kcpClusterClient.Cluster(logicalcluster.Wildcard)
	wildcardKcpInformers := kcpinformer.NewSharedInformerFactory(wildcardKcpClient, 10*time.Minute)

	// create apiserver
	virtualWorkspaces, err := o.VirtualWorkspaces.NewVirtualWorkspaces(identityConfig, o.RootPathPrefix, wildcardKubeInformers, wildcardKcpInformers)
	if err != nil {
		return err
	}
	scheme := runtime.NewScheme()
	metav1.AddToGroupVersion(scheme, schema.GroupVersion{Group: "", Version: "v1"})
	codecs := serializer.NewCodecFactory(scheme)
	recommendedConfig := genericapiserver.NewRecommendedConfig(codecs)
	if err := o.SecureServing.ApplyTo(&recommendedConfig.Config.SecureServing); err != nil {
		return err
	}
	if err := o.Authentication.ApplyTo(&recommendedConfig.Authentication, recommendedConfig.SecureServing, recommendedConfig.OpenAPIConfig); err != nil {
		return err
	}
	if err := o.Authorization.ApplyTo(&recommendedConfig.Config, virtualWorkspaces); err != nil {
		return err
	}
	rootAPIServerConfig, err := virtualrootapiserver.NewRootAPIConfig(recommendedConfig, []virtualrootapiserver.InformerStart{
		wildcardKubeInformers.Start,
		wildcardKcpInformers.Start,
	}, virtualWorkspaces)
	if err != nil {
		return err
	}

	completedRootAPIServerConfig := rootAPIServerConfig.Complete()
	rootAPIServer, err := completedRootAPIServerConfig.New(genericapiserver.NewEmptyDelegate())
	if err != nil {
		return err
	}
	preparedRootAPIServer := rootAPIServer.GenericAPIServer.PrepareRun()

	// this **must** be done after PrepareRun() as it sets up the openapi endpoints
	if err := completedRootAPIServerConfig.WithOpenAPIAggregationController(preparedRootAPIServer.GenericAPIServer); err != nil {
		return err
	}

	klog.Infof("Starting virtual workspace apiserver on %s (%s)", rootAPIServerConfig.GenericConfig.ExternalAddress, version.Get().String())

	return preparedRootAPIServer.Run(ctx.Done())
}

func readKubeConfig(kubeConfigFile string) (clientcmd.ClientConfig, error) {
	// Resolve relative to CWD
	absoluteKubeConfigFile, err := clientcmdapi.MakeAbs(kubeConfigFile, "")
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
