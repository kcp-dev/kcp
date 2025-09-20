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

package options

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	genericapiserveroptions "k8s.io/apiserver/pkg/server/options"
	"k8s.io/component-base/logs"
	logsapiv1 "k8s.io/component-base/logs/api/v1"

	cacheoptions "github.com/kcp-dev/kcp/pkg/cache/client/options"
	corevwoptions "github.com/kcp-dev/kcp/pkg/virtual/options"
)

// DefaultRootPathPrefix is basically constant forever, or we risk a breaking change. The
// kubectl plugin for example will use this prefix to generate the root path, and because
// we don't control kubectl plugin updates, we cannot change this prefix.
const DefaultRootPathPrefix string = "/services"

type Options struct {
	Output io.Writer

	KubeconfigFile   string
	Context          string
	RootPathPrefix   string
	ShardExternalURL string

	ShardVirtualWorkspaceCAFile string
	ShardVirtualWorkspaceURL    string
	ShardClientCertFile         string
	ShardClientKeyFile          string

	Cache          cacheoptions.Cache
	SecureServing  genericapiserveroptions.SecureServingOptions
	Authentication genericapiserveroptions.DelegatingAuthenticationOptions
	Authorization  corevwoptions.Authorization
	Audit          genericapiserveroptions.AuditOptions

	Logs *logs.Options

	CoreVirtualWorkspaces     corevwoptions.Options
	VirtualWorkspaceAdmission corevwoptions.Admission

	ProfilerAddress string
}

func NewOptions() *Options {
	opts := &Options{
		Output: nil,

		RootPathPrefix:   DefaultRootPathPrefix,
		ShardExternalURL: "",

		Cache:          *cacheoptions.NewCache(),
		SecureServing:  *genericapiserveroptions.NewSecureServingOptions(),
		Authentication: *genericapiserveroptions.NewDelegatingAuthenticationOptions(),
		Authorization:  *corevwoptions.NewAuthorization(),
		Audit:          *genericapiserveroptions.NewAuditOptions(),
		Logs:           logs.NewOptions(),

		CoreVirtualWorkspaces:     *corevwoptions.NewOptions(),
		VirtualWorkspaceAdmission: *corevwoptions.NewAdmission(),
		ProfilerAddress:           "",
	}

	opts.SecureServing.ServerCert.CertKey.CertFile = filepath.Join(".", ".kcp", "apiserver.crt")
	opts.SecureServing.ServerCert.CertKey.KeyFile = filepath.Join(".", ".kcp", "apiserver.key")
	opts.SecureServing.BindPort = 6444
	opts.Authentication.SkipInClusterLookup = true
	return opts
}

func (o *Options) AddFlags(flags *pflag.FlagSet) {
	o.Cache.AddFlags(flags)
	o.SecureServing.AddFlags(flags)
	o.Authentication.AddFlags(flags)
	o.Audit.AddFlags(flags)
	logsapiv1.AddFlags(o.Logs, flags)
	o.CoreVirtualWorkspaces.AddFlags(flags)

	flags.StringVar(&o.ShardExternalURL, "shard-external-url", o.ShardExternalURL, "URL used by outside clients to talk to the kcp shard this virtual workspace is related to")

	flags.StringVar(&o.KubeconfigFile, "kubeconfig", o.KubeconfigFile,
		"The kubeconfig file of the KCP instance that hosts workspaces.")
	_ = cobra.MarkFlagRequired(flags, "kubeconfig")

	flags.StringVar(&o.Context, "context", o.Context, "Name of the context in the kubeconfig file to use")
	flags.StringVar(&o.ProfilerAddress, "profiler-address", "", "[Address]:port to bind the profiler to")

	flags.StringVar(&o.ShardVirtualWorkspaceCAFile, "shard-virtual-workspace-ca-file", o.ShardVirtualWorkspaceCAFile, "Path to a CA certificate file that is valid for the virtual workspace server.")
	flags.StringVar(&o.ShardVirtualWorkspaceURL, "shard-virtual-workspace-url", o.ShardVirtualWorkspaceURL, "An external URL address of a virtual workspace server associated with this shard. Defaults to shard's base address.")
	flags.StringVar(&o.ShardClientCertFile, "shard-client-cert-file", o.ShardClientCertFile, "Path to a client certificate file the shard uses to communicate with other system components.")
	flags.StringVar(&o.ShardClientKeyFile, "shard-client-key-file", o.ShardClientKeyFile, "Path to a client certificate key file the shard uses to communicate with other system components.")

}

func (o *Options) Validate() error {
	errs := []error{}
	errs = append(errs, o.Cache.Validate()...)
	errs = append(errs, o.SecureServing.Validate()...)
	errs = append(errs, o.Authentication.Validate()...)
	errs = append(errs, o.CoreVirtualWorkspaces.Validate()...)

	if len(o.ShardExternalURL) == 0 {
		errs = append(errs, fmt.Errorf(("--shard-external-url is required")))
	}

	if len(o.KubeconfigFile) == 0 {
		errs = append(errs, fmt.Errorf("--kubeconfig is required for this command"))
	}
	if !strings.HasPrefix(o.RootPathPrefix, "/") {
		errs = append(errs, fmt.Errorf("RootPathPrefix %q must start with /", o.RootPathPrefix))
	}

	if o.ShardVirtualWorkspaceCAFile == "" {
		errs = append(errs, fmt.Errorf("ShardVirtualWorkspaceCAFile is required"))
	}
	if o.ShardVirtualWorkspaceURL == "" {
		errs = append(errs, fmt.Errorf("ShardVirtualWorkspaceURL is required"))
	}
	if o.ShardClientCertFile == "" {
		errs = append(errs, fmt.Errorf("ShardClientCertFile is required"))
	}
	if o.ShardClientKeyFile == "" {
		errs = append(errs, fmt.Errorf("ShardClientKeyFile is required"))
	}

	return utilerrors.NewAggregate(errs)
}
