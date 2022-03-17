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
	"os"
	"path/filepath"

	"github.com/spf13/pflag"

	"k8s.io/apiserver/pkg/authentication/request/x509"
	genericapiserver "k8s.io/apiserver/pkg/server"
	apiserveroptions "k8s.io/apiserver/pkg/server/options"

	proxyoptions "github.com/kcp-dev/kcp/pkg/proxy/options"
)

// ClientCertOptions wraps ClientCertAuthenticationOptions so we don't pull in
// more auth machinery than we need with DelegatingAuthenticationOptions
type ClientCertOptions struct {
	Options apiserveroptions.ClientCertAuthenticationOptions
}

// ApplyTo sets up the x509 Authenticator if the client-ca-file option was passed
func (c *ClientCertOptions) ApplyTo(authenticationInfo *genericapiserver.AuthenticationInfo, servingInfo *genericapiserver.SecureServingInfo) error {
	clientCAProvider, err := c.Options.GetClientCAContentProvider()
	if err != nil {
		return fmt.Errorf("unable to load client CA provider: %w", err)
	}
	if clientCAProvider != nil {
		if err = authenticationInfo.ApplyClientCert(clientCAProvider, servingInfo); err != nil {
			return fmt.Errorf("unable to assign client CA provider: %w", err)
		}
		authenticationInfo.Authenticator = x509.NewDynamic(clientCAProvider.VerifyOptions, x509.CommonNameUserConversion)
	}
	return nil
}

// AddFlags delegates to ClientCertAuthenticationOptions
func (c *ClientCertOptions) AddFlags(fs *pflag.FlagSet) {
	c.Options.AddFlags(fs)
}

// Validate just completes the options pattern. Returns nil.
func (c *ClientCertOptions) Validate() []error {
	return nil
}

type Options struct {
	SecureServing  apiserveroptions.SecureServingOptionsWithLoopback
	Authentication ClientCertOptions
	Proxy          proxyoptions.Options
	RootDirectory  string
}

func NewOptions() *Options {
	o := &Options{
		SecureServing:  *apiserveroptions.NewSecureServingOptions().WithLoopback(),
		Authentication: ClientCertOptions{},
		Proxy:          *proxyoptions.NewOptions(),

		RootDirectory: ".kcp",
	}

	// override all the things
	o.SecureServing.BindPort = 443
	o.SecureServing.ServerCert.CertDirectory = ""
	o.SecureServing.ServerCert.PairName = "apiserver" // we want to reuse the apiserver certs by default
	return o
}

func (o *Options) AddFlags(fs *pflag.FlagSet) {
	o.SecureServing.AddFlags(fs)
	o.Authentication.AddFlags(fs)
	o.Proxy.AddFlags(fs)

	fs.StringVar(&o.RootDirectory, "root-directory", o.RootDirectory, "Root directory.")
}

func (o *Options) Complete() error {
	if err := o.Proxy.Complete(); err != nil {
		return err
	}

	if !filepath.IsAbs(o.RootDirectory) {
		pwd, err := os.Getwd()
		if err != nil {
			return err
		}
		o.RootDirectory = filepath.Join(pwd, o.RootDirectory)
	}

	if len(o.SecureServing.ServerCert.CertDirectory) == 0 {
		o.SecureServing.ServerCert.CertDirectory = o.RootDirectory
	}
	if !filepath.IsAbs(o.SecureServing.ServerCert.CertDirectory) {
		o.SecureServing.ServerCert.CertDirectory = filepath.Join(o.RootDirectory, o.SecureServing.ServerCert.CertDirectory)
	}

	if err := o.SecureServing.MaybeDefaultWithSelfSignedCerts("localhost", []string{"kubernetes.default.svc", "kubernetes.default", "kubernetes"}, nil); err != nil {
		return err
	}

	return nil
}

func (o *Options) Validate() []error {
	var errs []error

	errs = append(errs, o.SecureServing.Validate()...)
	errs = append(errs, o.Authentication.Validate()...)
	errs = append(errs, o.Proxy.Validate()...)

	return errs
}
