/*
Copyright 2026 The kcp Authors.

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

package server

import (
	"errors"
	"fmt"

	"github.com/spf13/pflag"

	genericoptions "k8s.io/apiserver/pkg/server/options"

	vwoptions "github.com/kcp-dev/virtual-workspace-framework/pkg/options"
)

// Options configures the access virtual workspace server.
type Options struct {
	// SecureServing configures TLS serving (bind address, port,
	// serving certs). The VW must serve TLS: behind kcp's front-proxy
	// the proxy verifies the VW's serving cert, and the VW verifies
	// the proxy's client cert via the requestheader CA.
	SecureServing *genericoptions.SecureServingOptions

	// Authentication configures delegated authentication:
	//   - requestheader: trust X-Remote-User/X-Remote-Group/X-Remote-Extra-*
	//     from clients presenting a cert signed by --requestheader-client-ca-file
	//     (this is how kcp's front-proxy forwards identity),
	//   - bearer tokens: TokenReview against kcp (direct access without
	//     the front-proxy, e.g. local development),
	//   - client certs via --client-ca-file.
	Authentication *genericoptions.DelegatingAuthenticationOptions

	// Authorization is the virtual-workspace-framework authorizer setup:
	// always-allow paths (health endpoints) plus per-VW authorizers.
	Authorization *vwoptions.Authorization

	// Kubeconfig is the path to the kubeconfig for the target kcp.
	// Used by the RBAC provider (informers), for TokenReview-based
	// authentication, and as the base identity for impersonated
	// per-workspace MCP calls.
	Kubeconfig string

	// EndpointBase is the front-proxy URL prefix used to construct
	// per-cluster endpoints in SCAR responses.
	EndpointBase string

	// APIExportEndpointSlice is the name of the APIExportEndpointSlice
	// for the access VW's system APIExport. When set, the RBAC provider
	// runs in multi-shard mode and only indexes workspaces bound to
	// that APIExport.
	APIExportEndpointSlice string
}

// NewOptions returns options with defaults suitable for running behind
// kcp's front-proxy.
func NewOptions() *Options {
	o := &Options{
		SecureServing:  genericoptions.NewSecureServingOptions(),
		Authentication: genericoptions.NewDelegatingAuthenticationOptions(),
		Authorization:  vwoptions.NewAuthorization(),
	}

	o.SecureServing.BindPort = 9443
	o.SecureServing.ServerCert.PairName = "access-vw"
	o.Authentication.SkipInClusterLookup = true

	return o
}

// AddFlags registers all flags on the given flag set.
func (o *Options) AddFlags(fs *pflag.FlagSet) {
	o.SecureServing.AddFlags(fs)
	o.Authentication.AddFlags(fs)
	o.Authorization.AddFlags(fs)

	fs.StringVar(&o.EndpointBase, "endpoint-base", "https://kcp.example.com/clusters/",
		"FrontProxy URL prefix for cluster endpoints returned in SCAR responses.")
	fs.StringVar(&o.APIExportEndpointSlice, "apiexport-endpointslice", "",
		"Name of the APIExportEndpointSlice for the access VW's system APIExport. "+
			"When set, the RBAC provider runs in multi-shard mode via multicluster-runtime; "+
			"only workspaces with an APIBinding to that APIExport are indexed.")
	if fs.Lookup("kubeconfig") == nil {
		fs.StringVar(&o.Kubeconfig, "kubeconfig", "", "Path to the kubeconfig for the target kcp (required).")
	}
}

// Complete fills in derived defaults.
func (o *Options) Complete() error {
	if o.Kubeconfig == "" {
		return fmt.Errorf("--kubeconfig is required")
	}

	if o.Authentication.RemoteKubeConfigFile == "" {
		o.Authentication.RemoteKubeConfigFile = o.Kubeconfig
	}

	return nil
}

// Validate checks flag consistency, reporting every problem it finds
// rather than only the first.
func (o *Options) Validate() error {
	secureServingErrs := o.SecureServing.Validate()
	authenticationErrs := o.Authentication.Validate()
	authorizationErrs := o.Authorization.Validate()

	errs := make([]error, 0, len(secureServingErrs)+len(authenticationErrs)+len(authorizationErrs))
	errs = append(errs, secureServingErrs...)
	errs = append(errs, authenticationErrs...)
	errs = append(errs, authorizationErrs...)

	return errors.Join(errs...)
}
