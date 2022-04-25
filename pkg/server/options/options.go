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
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"k8s.io/apimachinery/pkg/util/sets"
	genericapiserveroptions "k8s.io/apiserver/pkg/server/options"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/kubernetes/pkg/genericcontrolplane/options"
	kubeoptions "k8s.io/kubernetes/pkg/kubeapiserver/options"

	kcpadmission "github.com/kcp-dev/kcp/pkg/admission"
	_ "github.com/kcp-dev/kcp/pkg/features"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
)

type Options struct {
	GenericControlPlane ServerRunOptions
	EmbeddedEtcd        EmbeddedEtcd
	Controllers         Controllers
	Authorization       Authorization
	AdminAuthentication AdminAuthentication
	Virtual             Virtual

	Extra ExtraOptions
}

type ExtraOptions struct {
	RootDirectory            string
	ProfilerAddress          string
	ShardKubeconfigFile      string
	EnableSharding           bool
	DiscoveryPollInterval    time.Duration
	ExperimentalBindFreePort bool
}

type completedOptions struct {
	GenericControlPlane options.CompletedServerRunOptions
	EmbeddedEtcd        EmbeddedEtcd
	Controllers         Controllers
	Authorization       Authorization
	AdminAuthentication AdminAuthentication
	Virtual             Virtual

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
		EmbeddedEtcd:        *NewEmbeddedEtcd(),
		Controllers:         *NewControllers(),
		Authorization:       *NewAuthorization(),
		AdminAuthentication: *NewAdminAuthentication(),
		Virtual:             *NewVirtual(),

		Extra: ExtraOptions{
			RootDirectory:            ".kcp",
			ProfilerAddress:          "",
			ShardKubeconfigFile:      "",
			EnableSharding:           false,
			DiscoveryPollInterval:    60 * time.Second,
			ExperimentalBindFreePort: false,
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
		WithServiceAccounts().
		WithTokenFile()
	//WithWebHook()
	o.GenericControlPlane.Authentication.ServiceAccounts.Issuers = []string{"https://kcp.default.svc"}
	o.GenericControlPlane.Etcd.StorageConfig.Transport.ServerList = []string{"embedded"}

	// override set of admission plugins
	kcpadmission.RegisterAllKcpAdmissionPlugins(o.GenericControlPlane.Admission.Plugins)
	o.GenericControlPlane.Admission.DisablePlugins = kcpadmission.DefaultOffAdmissionPlugins().List()
	o.GenericControlPlane.Admission.RecommendedPluginOrder = kcpadmission.AllOrderedPlugins

	return o
}

func (o *Options) Flags() cliflag.NamedFlagSets {
	fss := filter(o.rawFlags(), allowedFlags)

	// add flags that are filtered out from upstream, but overridden here with our own version
	fs := fss.FlagSet("KCP")
	fs.Var(kcpfeatures.NewFlagValue(), "features-gates", ""+
		"A set of key=value pairs that describe feature gates for alpha/experimental features. "+
		"Options are:\n"+strings.Join(kcpfeatures.KnownFeatures(), "\n")) // hide kube-only gates

	fss.Order = namedFlagSetOrder

	return fss
}

func (o *Options) rawFlags() cliflag.NamedFlagSets {
	fss := o.GenericControlPlane.Flags()

	etcdServers := fss.FlagSet("etcd").Lookup("etcd-servers")
	etcdServers.Usage += " By default an embedded etcd server is started."

	o.EmbeddedEtcd.AddFlags(fss.FlagSet("Embedded etcd"))
	o.Controllers.AddFlags(fss.FlagSet("KCP Controllers"))
	o.Authorization.AddFlags(fss.FlagSet("KCP Authorization"))
	o.AdminAuthentication.AddFlags(fss.FlagSet("KCP Authentication"))
	o.Virtual.AddFlags(fss.FlagSet("KCP Virtual Workspaces"))

	fs := fss.FlagSet("KCP")
	fs.StringVar(&o.Extra.ProfilerAddress, "profiler-address", o.Extra.ProfilerAddress, "[Address]:port to bind the profiler to")
	fs.StringVar(&o.Extra.ShardKubeconfigFile, "shard-kubeconfig-file", o.Extra.ShardKubeconfigFile, "Kubeconfig holding admin(!) credentials to peer kcp shards.")
	fs.BoolVar(&o.Extra.EnableSharding, "enable-sharding", o.Extra.EnableSharding, "Enable delegating to peer kcp shards.")
	fs.StringVar(&o.Extra.RootDirectory, "root-directory", o.Extra.RootDirectory, "Root directory.")
	fs.DurationVar(&o.Extra.DiscoveryPollInterval, "discovery-poll-interval", o.Extra.DiscoveryPollInterval, "Polling interval for dynamic discovery informers.")

	fs.BoolVar(&o.Extra.ExperimentalBindFreePort, "experimental-bind-free-port", o.Extra.ExperimentalBindFreePort, "Bind to a free port. --secure-port must be 0. Use the admin.kubeconfig to extract the chosen port.")
	fs.MarkHidden("experimental-bind-free-port") // nolint:errcheck

	return fss
}

func (o *CompletedOptions) Validate() []error {
	var errs []error

	if o.Extra.ExperimentalBindFreePort {
		if o.GenericControlPlane.SecureServing.BindPort != 0 {
			errs = append(errs, fmt.Errorf("--secure-port=0 required if --experimental-bind-free-port is set"))
		}
	}

	errs = append(errs, o.GenericControlPlane.Validate()...)
	errs = append(errs, o.Controllers.Validate()...)
	errs = append(errs, o.EmbeddedEtcd.Validate()...)
	errs = append(errs, o.Authorization.Validate()...)
	errs = append(errs, o.AdminAuthentication.Validate()...)
	errs = append(errs, o.Virtual.Validate()...)

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
	if !filepath.IsAbs(o.AdminAuthentication.TokenHashFilePath) {
		o.AdminAuthentication.TokenHashFilePath = filepath.Join(o.Extra.RootDirectory, o.AdminAuthentication.TokenHashFilePath)
	}
	if !filepath.IsAbs(o.AdminAuthentication.KubeConfigPath) {
		o.AdminAuthentication.KubeConfigPath = filepath.Join(o.Extra.RootDirectory, o.AdminAuthentication.KubeConfigPath)
	}

	if o.Extra.ExperimentalBindFreePort {
		listener, _, err := genericapiserveroptions.CreateListener("tcp", fmt.Sprintf("%s:0", o.GenericControlPlane.SecureServing.BindAddress), net.ListenConfig{})
		if err != nil {
			return nil, err
		}
		o.GenericControlPlane.SecureServing.Listener = listener
	}

	if err := o.Controllers.Complete(o.Extra.RootDirectory); err != nil {
		return nil, err
	}
	if o.Controllers.SAController.ServiceAccountKeyFile != "" && !filepath.IsAbs(o.Controllers.SAController.ServiceAccountKeyFile) {
		o.Controllers.SAController.ServiceAccountKeyFile = filepath.Join(o.Extra.RootDirectory, o.Controllers.SAController.ServiceAccountKeyFile)
	}
	if len(o.GenericControlPlane.Authentication.ServiceAccounts.KeyFiles) == 0 {
		o.GenericControlPlane.Authentication.ServiceAccounts.KeyFiles = []string{o.Controllers.SAController.ServiceAccountKeyFile}
	}
	if o.GenericControlPlane.ServerRunOptions.ServiceAccountSigningKeyFile == "" {
		o.GenericControlPlane.ServerRunOptions.ServiceAccountSigningKeyFile = o.Controllers.SAController.ServiceAccountKeyFile
	}

	completedGenericControlPlane, err := o.GenericControlPlane.ServerRunOptions.Complete()
	if err != nil {
		return nil, err
	}

	if o.Extra.ExperimentalBindFreePort {
		// Override Required here. It influences o.GenericControlPlane.Validate to pass without a set port,
		// but other than that only has cosmetic effects e.g. on the flag description. Hence, we do it here
		// in Complete and not in NewOptions.
		o.GenericControlPlane.SecureServing.Required = false
	}

	return &CompletedOptions{
		completedOptions: &completedOptions{
			// TODO: GenericControlPlane here should be completed. But the k/k repo does not expose the CompleteOptions type, but should.
			GenericControlPlane: completedGenericControlPlane,
			EmbeddedEtcd:        o.EmbeddedEtcd,
			Controllers:         o.Controllers,
			Authorization:       o.Authorization,
			AdminAuthentication: o.AdminAuthentication,
			Virtual:             o.Virtual,
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
