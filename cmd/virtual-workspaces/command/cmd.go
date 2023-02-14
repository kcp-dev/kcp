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
	"net/http"
	"net/url"
	"time"

	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	kcpkubernetesclient "github.com/kcp-dev/client-go/kubernetes"
	"github.com/spf13/cobra"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/wait"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/pkg/version"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/component-base/config"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/server/bootstrap"
	virtualrootapiserver "github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
)

func NewCommand(ctx context.Context, errout io.Writer) *cobra.Command {
	opts := options.NewOptions()

	// Default to -v=2
	opts.Logs.Config.Verbosity = config.VerbosityLevel(2)

	cmd := &cobra.Command{
		Use:   "workspaces",
		Short: "Launch virtual workspace apiservers",
		Long:  "Start the root virtual workspace apiserver to enable virtual workspace management.",

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
	logger := klog.FromContext(ctx).WithValues("component", "virtual-workspaces")
	// parse kubeconfig
	kubeConfig, err := readKubeConfig(o.KubeconfigFile, o.Context)
	if err != nil {
		return err
	}
	nonIdentityConfig, err := kubeConfig.ClientConfig()
	if err != nil {
		return err
	}

	// parse cache kubeconfig
	defaultCacheClientConfig, err := kubeConfig.ClientConfig()
	if err != nil {
		return err
	}
	cacheConfig, err := o.Cache.RestConfig(defaultCacheClientConfig)
	if err != nil {
		return err
	}
	cacheKcpClusterClient, err := kcpclientset.NewForConfig(cacheConfig)
	if err != nil {
		return err
	}

	// Don't throttle
	nonIdentityConfig.QPS = -1

	u, err := url.Parse(nonIdentityConfig.Host)
	if err != nil {
		return err
	}
	u.Path = ""
	nonIdentityConfig.Host = u.String()

	localShardKubeClusterClient, err := kcpkubernetesclient.NewForConfig(nonIdentityConfig)
	if err != nil {
		return err
	}

	// resolve identities for system APIBindings
	identityConfig, resolveIdentities := bootstrap.NewConfigWithWildcardIdentities(nonIdentityConfig, bootstrap.KcpRootGroupExportNames, bootstrap.KcpRootGroupResourceExportNames, localShardKubeClusterClient)
	if err := wait.PollImmediateInfiniteWithContext(ctx, time.Millisecond*500, func(ctx context.Context) (bool, error) {
		if err := resolveIdentities(ctx); err != nil {
			logger.V(3).Info("failed to resolve identities, keeping trying: ", "err", err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		return fmt.Errorf("failed to get or create identities: %w", err)
	}

	// create clients and informers
	kubeClusterClient, err := kcpkubernetesclient.NewForConfig(identityConfig)
	if err != nil {
		return err
	}

	wildcardKubeInformers := kcpkubernetesinformers.NewSharedInformerFactory(kubeClusterClient, 10*time.Minute)

	kcpClusterClient, err := kcpclientset.NewForConfig(identityConfig)
	if err != nil {
		return err
	}
	wildcardKcpInformers := kcpinformers.NewSharedInformerFactory(kcpClusterClient, 10*time.Minute)
	cacheKcpInformers := kcpinformers.NewSharedInformerFactory(cacheKcpClusterClient, 10*time.Minute)

	if o.ProfilerAddress != "" {
		//nolint:errcheck,gosec
		go http.ListenAndServe(o.ProfilerAddress, nil)
	}

	// create apiserver
	virtualWorkspaces, err := o.VirtualWorkspaces.NewVirtualWorkspaces(identityConfig, o.RootPathPrefix, wildcardKubeInformers, wildcardKcpInformers, cacheKcpInformers)
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
	if err := o.Audit.ApplyTo(&recommendedConfig.Config); err != nil {
		return err
	}
	rootAPIServerConfig, err := virtualrootapiserver.NewRootAPIConfig(recommendedConfig, []virtualrootapiserver.InformerStart{
		wildcardKubeInformers.Start,
		wildcardKcpInformers.Start,
		cacheKcpInformers.Start,
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

	logger.Info("Starting virtual workspace apiserver on ", "externalAddress", rootAPIServerConfig.GenericConfig.ExternalAddress, "version", version.Get().String())

	return preparedRootAPIServer.Run(ctx.Done())
}

func readKubeConfig(kubeConfigFile, context string) (clientcmd.ClientConfig, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	loadingRules.ExplicitPath = kubeConfigFile

	startingConfig, err := loadingRules.GetStartingConfig()
	if err != nil {
		return nil, err
	}

	overrides := &clientcmd.ConfigOverrides{
		CurrentContext: context,
	}

	clientConfig := clientcmd.NewDefaultClientConfig(*startingConfig, overrides)
	return clientConfig, nil
}
