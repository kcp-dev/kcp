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

	cacheoptions "github.com/kcp-dev/kcp/pkg/cache/client/options"
	virtualworkspacesoptions "github.com/kcp-dev/kcp/pkg/virtual/options"
)

// DefaultRootPathPrefix is basically constant forever, or we risk a breaking change. The
// kubectl plugin for example will use this prefix to generate the root path, and because
// we don't control kubectl plugin updates, we cannot change this prefix.
const DefaultRootPathPrefix string = "/services"

type Options struct {
	Output io.Writer

	KubeconfigFile string
	Context        string
	RootPathPrefix string

	Cache          cacheoptions.Cache
	SecureServing  genericapiserveroptions.SecureServingOptions
	Authentication genericapiserveroptions.DelegatingAuthenticationOptions
	Authorization  virtualworkspacesoptions.Authorization
	Audit          genericapiserveroptions.AuditOptions

	Logs logs.Options

	VirtualWorkspaces virtualworkspacesoptions.Options
	ProfilerAddress   string
}

func NewOptions() *Options {
	opts := &Options{
		Output: nil,

		RootPathPrefix: DefaultRootPathPrefix,

		Cache:          *cacheoptions.NewCache(),
		SecureServing:  *genericapiserveroptions.NewSecureServingOptions(),
		Authentication: *genericapiserveroptions.NewDelegatingAuthenticationOptions(),
		Authorization:  *virtualworkspacesoptions.NewAuthorization(),
		Audit:          *genericapiserveroptions.NewAuditOptions(),
		Logs:           *logs.NewOptions(),

		VirtualWorkspaces: *virtualworkspacesoptions.NewOptions(),
		ProfilerAddress:   "",
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
	o.Logs.AddFlags(flags)
	o.VirtualWorkspaces.AddFlags(flags)

	flags.StringVar(&o.KubeconfigFile, "kubeconfig", o.KubeconfigFile,
		"The kubeconfig file of the KCP instance that hosts workspaces.")
	_ = cobra.MarkFlagRequired(flags, "kubeconfig")

	flags.StringVar(&o.Context, "context", o.Context, "Name of the context in the kubeconfig file to use")
	flags.StringVar(&o.ProfilerAddress, "profiler-address", "", "[Address]:port to bind the profiler to")
}

func (o *Options) Validate() error {
	errs := []error{}
	errs = append(errs, o.Cache.Validate()...)
	errs = append(errs, o.SecureServing.Validate()...)
	errs = append(errs, o.Authentication.Validate()...)
	errs = append(errs, o.VirtualWorkspaces.Validate()...)

	if len(o.KubeconfigFile) == 0 {
		errs = append(errs, fmt.Errorf("--kubeconfig is required for this command"))
	}
	if !strings.HasPrefix(o.RootPathPrefix, "/") {
		errs = append(errs, fmt.Errorf("RootPathPrefix %q must start with /", o.RootPathPrefix))
	}

	return utilerrors.NewAggregate(errs)
}
