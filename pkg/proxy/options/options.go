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

	apiserveroptions "k8s.io/apiserver/pkg/server/options"
)

type Options struct {
	SecureServing         apiserveroptions.SecureServingOptionsWithLoopback
	Authentication        Authentication
	MappingFile           string
	RootDirectory         string
	RootKubeconfig        string
	ShardsKubeconfig      string
	ProfilerAddress       string
	CorsAllowedOriginList []string
}

func NewOptions() *Options {
	o := &Options{
		SecureServing:  *apiserveroptions.NewSecureServingOptions().WithLoopback(),
		Authentication: *NewAuthentication(),
		RootKubeconfig: "",
		RootDirectory:  ".kcp",
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
	fs.StringVar(&o.MappingFile, "mapping-file", o.MappingFile, "Config file mapping paths to backends")
	fs.StringVar(&o.RootDirectory, "root-directory", o.RootDirectory, "Root directory.")
	fs.StringVar(&o.RootKubeconfig, "root-kubeconfig", o.RootKubeconfig, "The path to the kubeconfig of the root shard.")
	fs.StringVar(&o.ShardsKubeconfig, "shards-kubeconfig", o.ShardsKubeconfig, "The path to the kubeconfig used for communication with all shards. The server name if provided is replaced with a shard's hostname.")
	fs.StringVar(&o.ProfilerAddress, "profiler-address", "", "[Address]:port to bind the profiler to")
	fs.StringSliceVar(&o.CorsAllowedOriginList, "cors-allowed-origins", o.CorsAllowedOriginList, "List of allowed origins for CORS, comma separated.  An allowed origin can be a regular expression to support subdomain matching. If this list is empty CORS will not be enabled.")
}

func (o *Options) Complete() error {
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

	return o.SecureServing.MaybeDefaultWithSelfSignedCerts("localhost", []string{"kubernetes.default.svc", "kubernetes.default", "kubernetes"}, nil)
}

func (o *Options) Validate() []error {
	var errs []error

	if o.MappingFile == "" {
		errs = append(errs, fmt.Errorf("--mapping-file is required"))
	}
	if len(o.ShardsKubeconfig) == 0 {
		errs = append(errs, fmt.Errorf("--shards-kubeconfig is required"))
	}

	errs = append(errs, o.SecureServing.Validate()...)
	errs = append(errs, o.Authentication.Validate()...)

	return errs
}
