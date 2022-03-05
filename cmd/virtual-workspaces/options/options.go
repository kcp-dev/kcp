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
	"io"
	"path/filepath"

	"github.com/spf13/pflag"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	genericapiserveroptions "k8s.io/apiserver/pkg/server/options"

	workspacesoptions "github.com/kcp-dev/kcp/pkg/virtual/workspaces/options"
)

type Options struct {
	Output io.Writer

	SecureServing  genericapiserveroptions.SecureServingOptions
	Authentication genericapiserveroptions.DelegatingAuthenticationOptions

	Workspaces workspacesoptions.Workspaces
}

func NewOptions() *Options {
	opts := &Options{
		Output:         nil,
		SecureServing:  *genericapiserveroptions.NewSecureServingOptions(),
		Authentication: *genericapiserveroptions.NewDelegatingAuthenticationOptions(),

		Workspaces: *workspacesoptions.NewWorkspaces(),
	}

	opts.SecureServing.ServerCert.CertKey.CertFile = filepath.Join(".", ".kcp", "apiserver.crt")
	opts.SecureServing.ServerCert.CertKey.KeyFile = filepath.Join(".", ".kcp", "apiserver.key")
	opts.SecureServing.BindPort = 6444
	opts.Authentication.SkipInClusterLookup = true
	return opts
}

func (o *Options) AddFlags(flags *pflag.FlagSet) {
	o.SecureServing.AddFlags(flags)
	o.Authentication.AddFlags(flags)
	o.Workspaces.AddGenericFlags(flags, "")
	o.Workspaces.AddStandaloneFlags(flags, "")
}

func (o *Options) Validate() error {
	errs := []error{}
	errs = append(errs, o.SecureServing.Validate()...)
	errs = append(errs, o.Authentication.Validate()...)
	errs = append(errs, o.Workspaces.Validate("")...)
	return utilerrors.NewAggregate(errs)
}
