/*
Copyright 2022 The kcp Authors.

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
	"time"

	"github.com/spf13/pflag"

	genericoptions "k8s.io/apiserver/pkg/server/options"
	"k8s.io/apiserver/pkg/storage/storagebackend"
	"k8s.io/component-base/logs"
	logsapiv1 "k8s.io/component-base/logs/api/v1"
	kubeoptions "k8s.io/kubernetes/pkg/kubeapiserver/options"

	etcdoptions "github.com/kcp-dev/embeddedetcd/options"
)

type Options struct {
	ServerRunOptions                   *genericoptions.ServerRunOptions
	Etcd                               *genericoptions.EtcdOptions
	SecureServing                      *genericoptions.SecureServingOptionsWithLoopback
	Authentication                     *Authentication
	Authorization                      *Authorization
	APIEnablement                      *genericoptions.APIEnablementOptions
	EmbeddedEtcd                       etcdoptions.Options
	Logs                               *logs.Options
	SyntheticDelay                     time.Duration
	ConversionCELTransformationTimeout time.Duration
}

type completedOptions struct {
	ServerRunOptions                   *genericoptions.ServerRunOptions
	Etcd                               *genericoptions.EtcdOptions
	SecureServing                      *genericoptions.SecureServingOptionsWithLoopback
	Authentication                     *Authentication
	Authorization                      *Authorization
	APIEnablement                      *genericoptions.APIEnablementOptions
	EmbeddedEtcd                       etcdoptions.CompletedOptions
	Logs                               *logs.Options
	SyntheticDelay                     time.Duration
	ConversionCELTransformationTimeout time.Duration
}

type CompletedOptions struct {
	*completedOptions
}

func (o *CompletedOptions) Validate() []error {
	errors := []error{} //nolint:prealloc
	errors = append(errors, o.ServerRunOptions.Validate()...)
	errors = append(errors, o.Etcd.Validate()...)
	errors = append(errors, o.SecureServing.Validate()...)
	errors = append(errors, o.Authentication.Validate()...)
	errors = append(errors, o.Authorization.Validate()...)
	errors = append(errors, o.APIEnablement.Validate()...)
	errors = append(errors, o.EmbeddedEtcd.Validate()...)
	return errors
}

// NewOptions creates a new Options with default parameters.
func NewOptions(rootDir string) *Options {
	o := &Options{
		ServerRunOptions:                   genericoptions.NewServerRunOptions(),
		Etcd:                               genericoptions.NewEtcdOptions(storagebackend.NewDefaultConfig(kubeoptions.DefaultEtcdPathPrefix, nil)),
		SecureServing:                      genericoptions.NewSecureServingOptions().WithLoopback(),
		Authentication:                     NewAuthentication(),
		Authorization:                      NewAuthorization(),
		APIEnablement:                      genericoptions.NewAPIEnablementOptions(),
		EmbeddedEtcd:                       *etcdoptions.NewOptions(rootDir),
		Logs:                               logs.NewOptions(),
		ConversionCELTransformationTimeout: time.Second,
	}

	o.SecureServing.ServerCert.CertDirectory = rootDir
	o.SecureServing.BindPort = 6443
	o.Etcd.StorageConfig.Transport.ServerList = []string{"embedded"}
	// TODO: enable the watch cache, it was disabled because
	//  - we need to pass a shard name so that the watch cache can calculate the key
	//    we already do that for cluster names (stored in the obj)
	//  - we need to modify wildcardClusterNameRegex and crdWildcardPartialMetadataClusterNameRegex
	o.Etcd.EnableWatchCache = false
	return o
}

func (o *Options) Complete() (*CompletedOptions, error) {
	if servers := o.Etcd.StorageConfig.Transport.ServerList; len(servers) == 1 && servers[0] == "embedded" {
		o.EmbeddedEtcd.Enabled = true
	}

	if err := o.SecureServing.MaybeDefaultWithSelfSignedCerts("localhost", nil, nil); err != nil {
		return nil, err
	}

	return &CompletedOptions{&completedOptions{
		ServerRunOptions:                   o.ServerRunOptions,
		Etcd:                               o.Etcd,
		SecureServing:                      o.SecureServing,
		Authentication:                     o.Authentication,
		Authorization:                      o.Authorization,
		APIEnablement:                      o.APIEnablement,
		EmbeddedEtcd:                       o.EmbeddedEtcd.Complete(o.Etcd),
		Logs:                               o.Logs,
		ConversionCELTransformationTimeout: o.ConversionCELTransformationTimeout,
	}}, nil
}

func (o *Options) AddFlags(fs *pflag.FlagSet) {
	o.Etcd.AddFlags(fs)
	o.EmbeddedEtcd.AddFlags(fs)
	o.SecureServing.AddFlags(fs)
	o.Authentication.AddFlags(fs)
	o.Authorization.AddFlags(fs)
	logsapiv1.AddFlags(o.Logs, fs)
	fs.DurationVar(&o.SyntheticDelay, "synthetic-delay", 0, "The duration of time the cache server will inject a delay for to all inbound requests. Useful for testing.")
}
