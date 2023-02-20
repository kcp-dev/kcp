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
	"time"

	"github.com/spf13/pflag"

	genericoptions "k8s.io/apiserver/pkg/server/options"
	"k8s.io/apiserver/pkg/storage/storagebackend"
	kubeoptions "k8s.io/kubernetes/pkg/kubeapiserver/options"

	etcdoptions "github.com/kcp-dev/kcp/pkg/embeddedetcd/options"
)

type Options struct {
	ServerRunOptions *genericoptions.ServerRunOptions
	Etcd             *genericoptions.EtcdOptions
	SecureServing    *genericoptions.SecureServingOptionsWithLoopback
	Authentication   *genericoptions.DelegatingAuthenticationOptions
	Authorization    *genericoptions.DelegatingAuthorizationOptions
	APIEnablement    *genericoptions.APIEnablementOptions
	EmbeddedEtcd     etcdoptions.Options
	SyntheticDelay   time.Duration
}

type completedOptions struct {
	ServerRunOptions *genericoptions.ServerRunOptions
	Etcd             *genericoptions.EtcdOptions
	SecureServing    *genericoptions.SecureServingOptionsWithLoopback
	Authentication   *genericoptions.DelegatingAuthenticationOptions
	Authorization    *genericoptions.DelegatingAuthorizationOptions
	APIEnablement    *genericoptions.APIEnablementOptions
	EmbeddedEtcd     etcdoptions.CompletedOptions
	SyntheticDelay   time.Duration
}

type CompletedOptions struct {
	*completedOptions
}

func (o *CompletedOptions) Validate() []error {
	errors := []error{}
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
		ServerRunOptions: genericoptions.NewServerRunOptions(),
		Etcd:             genericoptions.NewEtcdOptions(storagebackend.NewDefaultConfig(kubeoptions.DefaultEtcdPathPrefix, nil)),
		SecureServing:    genericoptions.NewSecureServingOptions().WithLoopback(),
		Authentication:   genericoptions.NewDelegatingAuthenticationOptions(),
		Authorization:    genericoptions.NewDelegatingAuthorizationOptions(),
		APIEnablement:    genericoptions.NewAPIEnablementOptions(),
		EmbeddedEtcd:     *etcdoptions.NewOptions(rootDir),
	}

	o.ServerRunOptions.EnablePriorityAndFairness = false
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

	// TODO: enable authN/Z stack
	o.Authentication = nil
	o.Authorization = nil

	if err := o.SecureServing.MaybeDefaultWithSelfSignedCerts("localhost", nil, nil); err != nil {
		return nil, err
	}

	return &CompletedOptions{&completedOptions{
		ServerRunOptions: o.ServerRunOptions,
		Etcd:             o.Etcd,
		SecureServing:    o.SecureServing,
		Authentication:   o.Authentication,
		Authorization:    o.Authorization,
		APIEnablement:    o.APIEnablement,
		EmbeddedEtcd:     o.EmbeddedEtcd.Complete(o.Etcd),
	}}, nil
}

func (o *Options) AddFlags(fs *pflag.FlagSet) {
	o.EmbeddedEtcd.AddFlags(fs)
	o.SecureServing.AddFlags(fs)
	fs.DurationVar(&o.SyntheticDelay, "synthetic-delay", 0, "The duration of time the cache server will inject a delay for to all inbound requests. Useful for testing.")
}
