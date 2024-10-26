/*
Copyright 2023 The KCP Authors.

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

	kcpkubernetesclient "github.com/kcp-dev/client-go/kubernetes"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/server/bootstrap"
	virtualrootapiserver "github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	virtualoptions "github.com/kcp-dev/kcp/pkg/virtual/options"
	"github.com/spf13/cobra"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/server/filters"
	"k8s.io/client-go/pkg/version"
	"k8s.io/client-go/tools/clientcmd"
	logsapiv1 "k8s.io/component-base/logs/api/v1"
	"k8s.io/klog/v2"

	proxyclientset "github.com/kcp-dev/kcp/contrib/mounts-vw/client/clientset/versioned/cluster"
	proxyinformers "github.com/kcp-dev/kcp/contrib/mounts-vw/client/informers/externalversions"
	"github.com/kcp-dev/kcp/contrib/mounts-vw/cmd/virtual-workspaces/options"
)

func NewCommand(ctx context.Context, errout io.Writer) *cobra.Command {
	opts := options.NewOptions()

	// Default to -v=2
	opts.Logs.Verbosity = logsapiv1.VerbosityLevel(2)

	cmd := &cobra.Command{
		Use:   "workspaces",
		Short: "Launch virtual cluster-proxy workspace apiservers",
		Long:  "Start the root virtual workspace apiserver to enable virtual workspace management.",

		RunE: func(c *cobra.Command, args []string) error {
			if err := logsapiv1.ValidateAndApply(opts.Logs, kcpfeatures.DefaultFeatureGate); err != nil {
				return err
			}
			if err := opts.Validate(); err != nil {
				return err
			}
			return nil
		},
	}

	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Start the virtual workspaces server",
		PersistentPreRunE: func(*cobra.Command, []string) error {
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.Validate(); err != nil {
				return err
			}

			return Start(ctx, opts)
		},
	}

	opts.AddFlags(cmd.Flags())
	opts.AddFlags(startCmd.Flags())

	cmd.AddCommand(startCmd)

	return cmd
}

// Start takes the options, starts the API server and waits until stopCh is closed or initial listening fails.
func Start(ctx context.Context, o *options.Options) error {
	logger := klog.FromContext(ctx).WithValues("component", "virtual-workspaces")
	// parse kubeconfig
	logger.Info("Starting virtual workspace apiserver")
	kubeConfig, err := readKubeConfig(o.KubeconfigFile, o.Context)
	if err != nil {
		return err
	}

	nonIdentityConfig, err := kubeConfig.ClientConfig()
	if err != nil {
		return err
	}

	// parse cache kubeconfig
	// TODO: add cache kubeconfig flag
	defaultCacheClientConfig, err := kubeConfig.ClientConfig()
	if err != nil {
		return err
	}
	cacheConfig, err := o.Cache.RestConfig(defaultCacheClientConfig)
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
	logger.Info("Resolving identities")
	identityConfig, resolveIdentities := bootstrap.NewConfigWithWildcardIdentities(nonIdentityConfig, bootstrap.KcpRootGroupExportNames, bootstrap.KcpRootGroupResourceExportNames, localShardKubeClusterClient)
	if err := wait.PollUntilContextCancel(ctx, time.Millisecond*500, true, func(ctx context.Context) (bool, error) {
		if err := resolveIdentities(ctx); err != nil {
			fmt.Println(err)
			logger.V(4).Info("failed to resolve identities, keeping trying: ", "err", err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		return fmt.Errorf("failed to get or create identities: %w", err)
	}
	logger.Info("Resolving identities done")

	cacheProxyClusterClient, err := proxyclientset.NewForConfig(cacheConfig)
	if err != nil {
		return err
	}
	cacheProxyInformers := proxyinformers.NewSharedInformerFactory(cacheProxyClusterClient, 10*time.Minute)

	if o.ProfilerAddress != "" {
		//nolint:errcheck,gosec
		go http.ListenAndServe(o.ProfilerAddress, nil)
	}

	// create apiserver
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
	if err := o.Audit.ApplyTo(&recommendedConfig.Config); err != nil {
		return err
	}

	rootAPIServerConfig, err := virtualrootapiserver.NewConfig(recommendedConfig)
	if err != nil {
		return err
	}

	authorizationOptions := virtualoptions.NewAuthorization()
	authorizationOptions.AlwaysAllowGroups = o.Authorization.AlwaysAllowGroups
	authorizationOptions.AlwaysAllowPaths = o.Authorization.AlwaysAllowPaths
	if err := authorizationOptions.ApplyTo(&recommendedConfig.Config, func() []virtualrootapiserver.NamedVirtualWorkspace {
		return rootAPIServerConfig.Extra.VirtualWorkspaces
	}); err != nil {
		return err
	}

	if err := o.Authorization.ApplyTo(&recommendedConfig.Config, func() []virtualrootapiserver.NamedVirtualWorkspace {
		return rootAPIServerConfig.Extra.VirtualWorkspaces
	}); err != nil {
		return err
	}

	logger.Info("Configuring virtual workspace apiserver")
	rootAPIServerConfig.Extra.VirtualWorkspaces, err = o.ProxyVirtualWorkspaces.NewVirtualWorkspaces(o.RootPathPrefix, identityConfig, cacheProxyInformers)
	if err != nil {
		return err
	}

	rootAPIServerConfig.Generic.LongRunningFunc = filters.BasicLongRunningRequestCheck(
		sets.NewString("watch", "proxy"),
		sets.NewString("attach", "exec", "proxy", "log", "portforward"),
	)

	completedRootAPIServerConfig := rootAPIServerConfig.Complete()
	rootAPIServer, err := virtualrootapiserver.NewServer(completedRootAPIServerConfig, genericapiserver.NewEmptyDelegate())
	if err != nil {
		return err
	}

	preparedRootAPIServer := rootAPIServer.GenericAPIServer.PrepareRun()

	// this **must** be done after PrepareRun() as it sets up the openapi endpoints
	if err := completedRootAPIServerConfig.WithOpenAPIAggregationController(preparedRootAPIServer.GenericAPIServer); err != nil {
		return err
	}

	logger.Info("Starting informers")
	cacheProxyInformers.Start(ctx.Done())

	cacheProxyInformers.WaitForCacheSync(ctx.Done())

	logger.Info("Starting virtual workspace apiserver on ", "externalAddress", rootAPIServerConfig.Generic.ExternalAddress, "version", version.Get().String())
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
