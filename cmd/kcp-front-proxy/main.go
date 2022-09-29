/*
Copyright 2022 The KCP Authors.

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
	goflags "flag"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"time"

	kcpclienthelper "github.com/kcp-dev/apimachinery/pkg/client"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	genericapifilters "k8s.io/apiserver/pkg/endpoints/filters"
	genericapiserver "k8s.io/apiserver/pkg/server"
	genericfilters "k8s.io/apiserver/pkg/server/filters"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/component-base/cli"
	utilflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/version"
	"k8s.io/klog/v2"

	frontproxyfilters "github.com/kcp-dev/kcp/cmd/kcp-front-proxy/filters"
	frontproxyoptions "github.com/kcp-dev/kcp/cmd/kcp-front-proxy/options"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/proxy"
	"github.com/kcp-dev/kcp/pkg/proxy/index"
	"github.com/kcp-dev/kcp/pkg/server"
	bootstrap "github.com/kcp-dev/kcp/pkg/server/bootstrap"
	"github.com/kcp-dev/kcp/pkg/server/requestinfo"
)

func main() {
	ctx := genericapiserver.SetupSignalContext()

	rand.Seed(time.Now().UTC().UnixNano())

	pflag.CommandLine.SetNormalizeFunc(utilflag.WordSepNormalizeFunc)
	pflag.CommandLine.AddGoFlagSet(goflags.CommandLine)

	cmd := NewProxyCommand(ctx)
	code := cli.Run(cmd)
	os.Exit(code)
}

func NewProxyCommand(ctx context.Context) *cobra.Command {
	options := frontproxyoptions.NewOptions()
	logger := klog.FromContext(ctx).WithValues("component", "front-proxy")
	cmd := &cobra.Command{
		Use:   "kcp-front-proxy",
		Short: "Terminate TLS and handles client cert auth for backend API servers",
		Long: `kcp-front-proxy is a reverse proxy that accepts client certificates and
forwards Common Name and Organizations to backend API servers in HTTP headers.
The proxy terminates TLS and communicates with API servers via mTLS. Traffic is
routed based on paths.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := options.Logs.ValidateAndApply(kcpfeatures.DefaultFeatureGate); err != nil {
				return err
			}
			if err := options.Complete(); err != nil {
				return err
			}
			if errs := options.Validate(); errs != nil {
				return errors.NewAggregate(errs)
			}

			if options.Proxy.ProfilerAddress != "" {
				//nolint:errcheck
				go http.ListenAndServe(options.Proxy.ProfilerAddress, nil)
			}

			var servingInfo *genericapiserver.SecureServingInfo
			var loopbackClientConfig *restclient.Config
			if err := options.Proxy.SecureServing.ApplyTo(&servingInfo, &loopbackClientConfig); err != nil {
				return err
			}
			var authenticationInfo genericapiserver.AuthenticationInfo
			if err := options.Proxy.Authentication.ApplyTo(&authenticationInfo, servingInfo); err != nil {
				return err
			}

			// get root API identities
			nonIdentityRootConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: options.Proxy.RootKubeconfig}, nil).ClientConfig()
			if err != nil {
				return fmt.Errorf("failed to load root kubeconfig: %w", err)
			}

			rootShardConfig, resolveIdentities := bootstrap.NewConfigWithWildcardIdentities(nonIdentityRootConfig, bootstrap.KcpRootGroupExportNames, bootstrap.KcpRootGroupResourceExportNames, nil)
			if err := wait.PollImmediateInfiniteWithContext(ctx, time.Millisecond*500, func(ctx context.Context) (bool, error) {
				if err := resolveIdentities(ctx); err != nil {
					logger.V(3).Info("failed to resolve identities, keeping trying", "err", err)
					return false, nil
				}
				return true, nil
			}); err != nil {
				return fmt.Errorf("failed to get or create identities: %w", err)
			}

			rootShardConfigInformerConfig := kcpclienthelper.SetCluster(restclient.CopyConfig(rootShardConfig), tenancyv1alpha1.RootCluster)
			rootShardConfigInformerClient, err := kcpclient.NewForConfig(rootShardConfigInformerConfig)
			if err != nil {
				return fmt.Errorf("failed to create client for informers: %w", err)
			}

			// start index
			kcpSharedInformerFactory := kcpinformers.NewSharedInformerFactoryWithOptions(rootShardConfigInformerClient, 30*time.Minute)
			indexController := index.NewController(
				ctx,
				rootShardConfig.Host,
				kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaceShards(),
				func(shard *tenancyv1alpha1.ClusterWorkspaceShard) (kcpclient.Interface, error) {
					shardConfig := restclient.CopyConfig(rootShardConfig)
					shardConfig.Host = shard.Spec.BaseURL
					shardClient, err := kcpclient.NewForConfig(kcpclienthelper.SetCluster(restclient.CopyConfig(shardConfig), logicalcluster.Wildcard))
					if err != nil {
						return nil, fmt.Errorf("failed to create shard %q client: %w", shard.Name, err)
					}
					return shardClient, nil
				},
			)

			go indexController.Start(ctx, 2)

			kcpSharedInformerFactory.Start(ctx.Done())
			kcpSharedInformerFactory.WaitForCacheSync(ctx.Done())

			// start the server
			handler, err := proxy.NewHandler(ctx, &options.Proxy, indexController)
			if err != nil {
				return err
			}
			failedHandler := frontproxyfilters.NewUnauthorizedHandler()
			handler = frontproxyfilters.WithOptionalClientCert(handler, failedHandler, authenticationInfo.Authenticator)

			requestInfoFactory := requestinfo.NewFactory()
			handler = server.WithInClusterServiceAccountRequestRewrite(handler)
			handler = genericapifilters.WithRequestInfo(handler, requestInfoFactory)
			handler = genericfilters.WithHTTPLogging(handler)
			handler = genericfilters.WithPanicRecovery(handler, requestInfoFactory)
			doneCh, _, err := servingInfo.Serve(handler, time.Second*60, ctx.Done())
			if err != nil {
				return err
			}

			<-doneCh
			return nil
		},
	}

	options.AddFlags(cmd.Flags())

	if v := version.Get().String(); len(v) == 0 {
		cmd.Version = "<unknown>"
	} else {
		cmd.Version = v
	}

	return cmd
}
