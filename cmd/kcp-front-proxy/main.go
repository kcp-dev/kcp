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
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	genericapifilters "k8s.io/apiserver/pkg/endpoints/filters"
	apirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	genericfilters "k8s.io/apiserver/pkg/server/filters"
	restclient "k8s.io/client-go/rest"
	utilflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/logs"
	"k8s.io/component-base/version"
	"k8s.io/klog/v2"

	frontproxyoptions "github.com/kcp-dev/kcp/cmd/kcp-front-proxy/options"
	"github.com/kcp-dev/kcp/pkg/proxy"
)

func main() {
	ctx := genericapiserver.SetupSignalContext()

	rand.Seed(time.Now().UTC().UnixNano())

	pflag.CommandLine.SetNormalizeFunc(utilflag.WordSepNormalizeFunc)
	pflag.CommandLine.AddGoFlagSet(goflags.CommandLine)

	logs.InitLogs()
	defer logs.FlushLogs()

	command := NewProxyCommand(ctx)
	if err := command.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func NewRequestInfoResolver() *apirequest.RequestInfoFactory {
	apiPrefixes := sets.NewString(strings.Trim(genericapiserver.APIGroupPrefix, "/"))
	legacyAPIPrefixes := sets.String{}
	apiPrefixes.Insert(strings.Trim(genericapiserver.DefaultLegacyAPIPrefix, "/"))
	legacyAPIPrefixes.Insert(strings.Trim(genericapiserver.DefaultLegacyAPIPrefix, "/"))

	return &apirequest.RequestInfoFactory{
		APIPrefixes:          apiPrefixes,
		GrouplessAPIPrefixes: legacyAPIPrefixes,
	}
}

func WithOptionalClientCert(handler, failed http.Handler, auth authenticator.Request) http.Handler {
	if auth == nil {
		return handler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.TLS == nil || len(req.TLS.PeerCertificates) == 0 {
			handler.ServeHTTP(w, req)
			return
		}
		_, ok, err := auth.AuthenticateRequest(req)
		if err != nil || !ok {
			if err != nil {
				klog.ErrorS(err, "Unable to authenticate the request")
			}
			failed.ServeHTTP(w, req)
			return
		}
		handler.ServeHTTP(w, req)
	})
}

func NewProxyCommand(ctx context.Context) *cobra.Command {
	options := frontproxyoptions.NewOptions()
	cmd := &cobra.Command{
		Use:   "kcp-front-proxy",
		Short: "Terminate TLS and handles client cert auth for backend API servers",
		Long: `kcp-front-proxy is a reverse proxy that accepts client certificates and
forwards Common Name and Organizations to backend API servers in HTTP headers.
The proxy terminates TLS and communicates with API servers via mTLS. Traffic is
routed based on paths.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := options.Complete(); err != nil {
				return err
			}
			if errs := options.Validate(); errs != nil {
				return errors.NewAggregate(errs)
			}
			var handler http.Handler
			handler, err := proxy.NewHandler(&options.Proxy)
			if err != nil {
				return err
			}

			var requestInfoResolver apirequest.RequestInfoResolver = NewRequestInfoResolver()
			var authenticationInfo genericapiserver.AuthenticationInfo
			var servingInfo *genericapiserver.SecureServingInfo
			var loopbackClientConfig *restclient.Config
			if err := options.SecureServing.ApplyTo(&servingInfo, &loopbackClientConfig); err != nil {
				return err
			}
			if err := options.Authentication.ApplyTo(&authenticationInfo, servingInfo); err != nil {
				return err
			}

			scheme := runtime.NewScheme()
			metav1.AddToGroupVersion(scheme, schema.GroupVersion{Group: "", Version: "v1"})
			codecs := serializer.NewCodecFactory(scheme)
			failedHandler := genericapifilters.Unauthorized(codecs)

			handler = WithOptionalClientCert(handler, failedHandler, authenticationInfo.Authenticator)
			handler = genericapifilters.WithRequestInfo(handler, requestInfoResolver)
			handler = genericfilters.WithPanicRecovery(handler, requestInfoResolver)

			doneCh, err := servingInfo.Serve(handler, time.Second*60, ctx.Done())
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
