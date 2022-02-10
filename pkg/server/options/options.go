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
	"time"

	"github.com/spf13/pflag"

	"k8s.io/apimachinery/pkg/util/sets"
	cliflag "k8s.io/component-base/cli/flag"
	_ "k8s.io/kubernetes/pkg/features"
	"k8s.io/kubernetes/pkg/genericcontrolplane/options"
	kubeoptions "k8s.io/kubernetes/pkg/kubeapiserver/options"
)

type Options struct {
	GenericControlPlane ServerRunOptions
	EmbeddedEtcd        EmbeddedEtcd
	Controllers         Controllers
	Authorization       Authorization

	Extra ExtraOptions
}

type ExtraOptions struct {
	KubeConfigPath        string
	RootDirectory         string
	ProfilerAddress       string
	ShardKubeconfigFile   string
	EnableSharding        bool
	DiscoveryPollInterval time.Duration
}

type completedOptions struct {
	GenericControlPlane options.CompletedServerRunOptions
	EmbeddedEtcd        EmbeddedEtcd
	Controllers         Controllers
	Authorization       Authorization

	Extra ExtraOptions
}

type CompletedOptions struct {
	*completedOptions
}

// NewOptions creates a new Options with default parameters.
func NewOptions() *Options {
	o := &Options{
		GenericControlPlane: ServerRunOptions{
			*options.NewServerRunOptions(),
		},
		EmbeddedEtcd:  *NewEmbeddedEtcd(),
		Controllers:   *NewControllers(),
		Authorization: *NewAuthorization(),

		Extra: ExtraOptions{
			KubeConfigPath:        "admin.kubeconfig",
			RootDirectory:         ".kcp",
			ProfilerAddress:       "",
			ShardKubeconfigFile:   "",
			EnableSharding:        false,
			DiscoveryPollInterval: 60 * time.Second,
		},
	}

	// override all the stuff
	o.GenericControlPlane.SecureServing.ServerCert.CertDirectory = "" // will be completed to be relative to root dir
	o.GenericControlPlane.Authentication = kubeoptions.NewBuiltInAuthenticationOptions().
		WithAnonymous().
		WithBootstrapToken().
		WithClientCert().
		WithOIDC().
		WithRequestHeader().
		// WithServiceAccounts().
		WithTokenFile()
	//WithWebHook()
	o.GenericControlPlane.Etcd.StorageConfig.Transport.ServerList = []string{"embedded"}

	return o
}

func (o *Options) Flags() (fss cliflag.NamedFlagSets) {
	return filter(o.rawFlags(), allowedFlags)
}

func (o *Options) rawFlags() cliflag.NamedFlagSets {
	fss := o.GenericControlPlane.Flags()

	etcdServers := fss.FlagSet("etcd").Lookup("etcd-servers")
	etcdServers.Usage += " By default an embedded etcd server is started."

	o.EmbeddedEtcd.AddFlags(fss.FlagSet("Embedded etcd"))
	o.Controllers.AddFlags(fss.FlagSet("KCP Controllers"))
	o.Authorization.AddFlags(fss.FlagSet("KCP Authorization"))

	fs := fss.FlagSet("KCP")
	fs.StringVar(&o.Extra.ProfilerAddress, "profiler-address", o.Extra.ProfilerAddress, "[Address]:port to bind the profiler to")
	fs.StringVar(&o.Extra.ShardKubeconfigFile, "shard-kubeconfig-file", o.Extra.ShardKubeconfigFile, "Kubeconfig holding admin(!) credentials to peer kcp shards.")
	fs.BoolVar(&o.Extra.EnableSharding, "enable-sharding", o.Extra.EnableSharding, "Enable delegating to peer kcp shards.")
	fs.StringVar(&o.Extra.RootDirectory, "root-directory", o.Extra.RootDirectory, "Root directory.")
	fs.DurationVar(&o.Extra.DiscoveryPollInterval, "discovery-poll-interval", o.Extra.DiscoveryPollInterval, "Polling interval for dynamic discovery informers.")
	fs.StringVar(&o.Extra.KubeConfigPath, "kubeconfig-path", o.Extra.KubeConfigPath, "Path to which the administrative kubeconfig should be written at startup.")

	return fss
}

func (o *CompletedOptions) Validate() []error {
	var errs []error

	errs = append(errs, o.GenericControlPlane.Validate()...)
	errs = append(errs, o.Controllers.Validate()...)
	errs = append(errs, o.EmbeddedEtcd.Validate()...)
	errs = append(errs, o.Authorization.Validate()...)

	if o.Extra.DiscoveryPollInterval == 0 {
		errs = append(errs, fmt.Errorf("--discovery-poll-interval not set"))
	}

	return errs
}

func (o *Options) Complete() (*CompletedOptions, error) {
	if servers := o.GenericControlPlane.Etcd.StorageConfig.Transport.ServerList; len(servers) == 1 && servers[0] == "embedded" {
		o.GenericControlPlane.Etcd.StorageConfig.Transport.ServerList = []string{"localhost:" + o.EmbeddedEtcd.ClientPort}
		o.EmbeddedEtcd.Enabled = true
	} else {
		o.EmbeddedEtcd.Enabled = false
	}

	if !filepath.IsAbs(o.Extra.RootDirectory) {
		pwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		o.Extra.RootDirectory = filepath.Join(pwd, o.Extra.RootDirectory)
	}
	if !filepath.IsAbs(o.EmbeddedEtcd.Directory) {
		o.EmbeddedEtcd.Directory = filepath.Join(o.Extra.RootDirectory, o.EmbeddedEtcd.Directory)
	}
	if !filepath.IsAbs(o.GenericControlPlane.SecureServing.ServerCert.CertDirectory) {
		o.GenericControlPlane.SecureServing.ServerCert.CertDirectory = filepath.Join(o.Extra.RootDirectory, o.GenericControlPlane.SecureServing.ServerCert.CertDirectory)
	}
	if len(o.GenericControlPlane.SecureServing.ServerCert.CertDirectory) == 0 {
		o.GenericControlPlane.SecureServing.ServerCert.CertDirectory = filepath.Join(o.Extra.RootDirectory, "certs")
	}

	completedGenericControlPlane, err := o.GenericControlPlane.ServerRunOptions.Complete()
	if err != nil {
		return nil, err
	}

	return &CompletedOptions{
		completedOptions: &completedOptions{
			// TODO: GenericControlPlane here should be completed. But the k/k repo does not expose the CompleteOptions type, but should.
			GenericControlPlane: completedGenericControlPlane,
			EmbeddedEtcd:        o.EmbeddedEtcd,
			Controllers:         o.Controllers,
			Authorization:       o.Authorization,
			Extra:               o.Extra,
		},
	}, nil
}

func filter(ffs cliflag.NamedFlagSets, allowed sets.String) cliflag.NamedFlagSets {
	filtered := cliflag.NamedFlagSets{}
	for title, fs := range ffs.FlagSets {
		section := filtered.FlagSet(title)
		fs.VisitAll(func(f *pflag.Flag) {
			if allowed.Has(f.Name) {
				section.AddFlag(f)
			}
		})
	}
	return filtered
}
