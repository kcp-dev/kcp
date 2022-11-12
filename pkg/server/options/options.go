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
	"errors"
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
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/genericcontrolplane/options"
	kubeoptions "k8s.io/kubernetes/pkg/kubeapiserver/options"

	kcpadmission "github.com/kcp-dev/kcp/pkg/admission"
	etcdoptions "github.com/kcp-dev/kcp/pkg/embeddedetcd/options"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/server/options/batteries"
)

type Options struct {
	GenericControlPlane ServerRunOptions
	EmbeddedEtcd        etcdoptions.Options
	Controllers         Controllers
	Authorization       Authorization
	AdminAuthentication AdminAuthentication
	Virtual             Virtual
	HomeWorkspaces      HomeWorkspaces
	Cache               Cache

	Extra ExtraOptions
}

type ExtraOptions struct {
	RootDirectory                 string
	ProfilerAddress               string
	ShardKubeconfigFile           string
	RootShardKubeconfigFile       string
	ShardBaseURL                  string
	ShardExternalURL              string
	ShardName                     string
	ShardVirtualWorkspaceURL      string
	DiscoveryPollInterval         time.Duration
	ExperimentalBindFreePort      bool
	LogicalClusterAdminKubeconfig string

	BatteriesIncluded []string
}

type completedOptions struct {
	GenericControlPlane options.CompletedServerRunOptions
	EmbeddedEtcd        etcdoptions.CompletedOptions
	Controllers         Controllers
	Authorization       Authorization
	AdminAuthentication AdminAuthentication
	Virtual             Virtual
	HomeWorkspaces      HomeWorkspaces
	Cache               cacheCompleted

	Extra ExtraOptions
}

type CompletedOptions struct {
	*completedOptions
}

// NewOptions creates a new Options with default parameters.
func NewOptions(rootDir string) *Options {
	o := &Options{
		GenericControlPlane: ServerRunOptions{
			*options.NewServerRunOptions(),
		},
		EmbeddedEtcd:        *etcdoptions.NewOptions(rootDir),
		Controllers:         *NewControllers(),
		Authorization:       *NewAuthorization(),
		AdminAuthentication: *NewAdminAuthentication(rootDir),
		Virtual:             *NewVirtual(),
		HomeWorkspaces:      *NewHomeWorkspaces(),
		Cache:               *NewCache(rootDir),

		Extra: ExtraOptions{
			RootDirectory:            rootDir,
			ProfilerAddress:          "",
			ShardKubeconfigFile:      "",
			ShardBaseURL:             "",
			ShardExternalURL:         "",
			ShardName:                "root",
			DiscoveryPollInterval:    60 * time.Second,
			ExperimentalBindFreePort: false,
			BatteriesIncluded:        batteries.Defaults.List(),
		},
	}

	// override all the stuff
	o.GenericControlPlane.SecureServing.ServerCert.CertDirectory = rootDir
	o.GenericControlPlane.Authentication = kubeoptions.NewBuiltInAuthenticationOptions().
		WithAnonymous().
		WithBootstrapToken().
		WithClientCert().
		WithOIDC().
		WithRequestHeader().
		WithServiceAccounts().
		WithTokenFile()
	// WithWebHook()
	o.GenericControlPlane.Authentication.ServiceAccounts.Issuers = []string{"https://kcp.default.svc"}
	o.GenericControlPlane.Etcd.StorageConfig.Transport.ServerList = []string{"embedded"}

	// override set of admission plugins
	kcpadmission.RegisterAllKcpAdmissionPlugins(o.GenericControlPlane.Admission.Plugins)
	o.GenericControlPlane.Admission.DisablePlugins = kcpadmission.DefaultOffAdmissionPlugins().List()
	o.GenericControlPlane.Admission.RecommendedPluginOrder = kcpadmission.AllOrderedPlugins

	// turn on the watch cache
	o.GenericControlPlane.Etcd.EnableWatchCache = true

	return o
}

func (o *Options) Flags() cliflag.NamedFlagSets {
	fss := filter(o.rawFlags(), allowedFlags)

	// add flags that are filtered out from upstream, but overridden here with our own version
	fs := fss.FlagSet("KCP")
	fs.Var(kcpfeatures.NewFlagValue(), "feature-gates", ""+
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
	o.HomeWorkspaces.AddFlags(fss.FlagSet("KCP Home Workspaces"))
	o.Cache.AddFlags(fss.FlagSet("KCP Cache Server"))

	fs := fss.FlagSet("KCP")
	fs.StringVar(&o.Extra.ProfilerAddress, "profiler-address", o.Extra.ProfilerAddress, "[Address]:port to bind the profiler to")
	fs.StringVar(&o.Extra.ShardKubeconfigFile, "shard-kubeconfig-file", o.Extra.ShardKubeconfigFile, "Kubeconfig holding admin(!) credentials to peer kcp shards.")
	fs.StringVar(&o.Extra.RootShardKubeconfigFile, "root-shard-kubeconfig-file", o.Extra.RootShardKubeconfigFile, "Kubeconfig holding admin(!) credentials to the root kcp shard.")
	fs.StringVar(&o.Extra.ShardBaseURL, "shard-base-url", o.Extra.ShardBaseURL, "Base URL to this kcp shard. Defaults to external address.")
	fs.StringVar(&o.Extra.ShardExternalURL, "shard-external-url", o.Extra.ShardExternalURL, "URL used by outside clients to talk to this kcp shard. Defaults to external address.")
	fs.StringVar(&o.Extra.ShardName, "shard-name", o.Extra.ShardName, "A name of this kcp shard. Defaults to the \"root\" name.")
	fs.StringVar(&o.Extra.ShardVirtualWorkspaceURL, "shard-virtual-workspace-url", o.Extra.ShardVirtualWorkspaceURL, "An external URL address of a virtual workspace server associated with this shard. Defaults to shard's base address.")
	fs.StringVar(&o.Extra.RootDirectory, "root-directory", o.Extra.RootDirectory, "Root directory.")
	fs.StringVar(&o.Extra.LogicalClusterAdminKubeconfig, "logical-cluster-admin-kubeconfig", o.Extra.LogicalClusterAdminKubeconfig, "Kubeconfig holding admin(!) credentials to other shards. Defaults to the loopback client")

	fs.BoolVar(&o.Extra.ExperimentalBindFreePort, "experimental-bind-free-port", o.Extra.ExperimentalBindFreePort, "Bind to a free port. --secure-port must be 0. Use the admin.kubeconfig to extract the chosen port.")
	fs.MarkHidden("experimental-bind-free-port") //nolint:errcheck

	fs.StringSliceVar(&o.Extra.BatteriesIncluded, "batteries-included", o.Extra.BatteriesIncluded, fmt.Sprintf(
		`A list of batteries included (= default objects that might be unwanted in production, but are very helpful in trying out kcp or for development). These are the possible values: %s.

- cluster-workspace-types: creates "organization" and "team" ClusterWorkspaceTypes in the root workspace.
- root-compute-workspace:  create a root:compute workspace, and kubernetes APIExport in it for deployments/services/ingresses
- user:                    creates an additional non-admin user and context named "user" in the admin.kubeconfig

Prefixing with - or + means to remove from the default set or add to the default set.`,
		strings.Join(batteries.All.List(), ","),
	))

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
	errs = append(errs, o.HomeWorkspaces.Validate()...)
	errs = append(errs, o.Cache.Validate()...)

	differential := false
	for i, b := range o.Extra.BatteriesIncluded {
		if strings.HasPrefix(b, "+") || strings.HasPrefix(b, "-") {
			if !differential && i > 0 {
				errs = append(errs, fmt.Errorf("--batteries-included must all be prefixed with + or - or none"))
				break
			}
			differential = true
			b = b[1:]
		} else if differential {
			errs = append(errs, fmt.Errorf("--batteries-included must all be prefixed with + or - or none"))
			break
		}
		if !batteries.All.Has(b) {
			errs = append(errs, fmt.Errorf("unknown battery: %s", b))
		}
	}

	if o.Extra.LogicalClusterAdminKubeconfig != "" && o.Extra.ShardExternalURL == "" {
		errs = append(errs, fmt.Errorf("--shard-external-url is required if --logical-cluster-admin-kubeconfig is set"))
	}

	return errs
}

func (o *Options) Complete() (*CompletedOptions, error) {
	if servers := o.GenericControlPlane.Etcd.StorageConfig.Transport.ServerList; len(servers) == 1 && servers[0] == "embedded" {
		o.EmbeddedEtcd.Enabled = true
	}

	if !filepath.IsAbs(o.Extra.RootDirectory) {
		pwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		o.Extra.RootDirectory = filepath.Join(pwd, o.Extra.RootDirectory)
	}

	// Create the configuration root correctly before other components get a chance.
	if err := mkdirRoot(o.Extra.RootDirectory); err != nil {
		return nil, err
	}

	var err error
	if !filepath.IsAbs(o.EmbeddedEtcd.Directory) {
		o.EmbeddedEtcd.Directory, err = filepath.Abs(o.EmbeddedEtcd.Directory)
		if err != nil {
			return nil, err
		}
	}
	if !filepath.IsAbs(o.GenericControlPlane.SecureServing.ServerCert.CertDirectory) {
		o.GenericControlPlane.SecureServing.ServerCert.CertDirectory, err = filepath.Abs(o.GenericControlPlane.SecureServing.ServerCert.CertDirectory)
		if err != nil {
			return nil, err
		}
	}
	if !filepath.IsAbs(o.AdminAuthentication.ShardAdminTokenHashFilePath) {
		o.AdminAuthentication.ShardAdminTokenHashFilePath, err = filepath.Abs(o.AdminAuthentication.ShardAdminTokenHashFilePath)
		if err != nil {
			return nil, err
		}
	}
	if !filepath.IsAbs(o.AdminAuthentication.KubeConfigPath) {
		o.AdminAuthentication.KubeConfigPath, err = filepath.Abs(o.AdminAuthentication.KubeConfigPath)
		if err != nil {
			return nil, err
		}
	}
	if len(o.Extra.LogicalClusterAdminKubeconfig) > 0 && !filepath.IsAbs(o.Extra.LogicalClusterAdminKubeconfig) {
		o.Extra.LogicalClusterAdminKubeconfig, err = filepath.Abs(o.Extra.LogicalClusterAdminKubeconfig)
		if err != nil {
			return nil, err
		}
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
		o.Controllers.SAController.ServiceAccountKeyFile, err = filepath.Abs(o.Controllers.SAController.ServiceAccountKeyFile)
		if err != nil {
			return nil, err
		}
	}
	if len(o.GenericControlPlane.Authentication.ServiceAccounts.KeyFiles) == 0 {
		o.GenericControlPlane.Authentication.ServiceAccounts.KeyFiles = []string{o.Controllers.SAController.ServiceAccountKeyFile}
	}
	if o.GenericControlPlane.ServerRunOptions.ServiceAccountSigningKeyFile == "" {
		o.GenericControlPlane.ServerRunOptions.ServiceAccountSigningKeyFile = o.Controllers.SAController.ServiceAccountKeyFile
	}

	completedGenericServerRunOptions, err := o.GenericControlPlane.ServerRunOptions.Complete()
	if err != nil {
		return nil, err
	}

	if o.Extra.ExperimentalBindFreePort {
		// Override Required here. It influences o.GenericControlPlane.Validate to pass without a set port,
		// but other than that only has cosmetic effects e.g. on the flag description. Hence, we do it here
		// in Complete and not in NewOptions.
		o.GenericControlPlane.SecureServing.Required = false
	}

	differential := false
	for _, b := range o.Extra.BatteriesIncluded {
		if strings.HasPrefix(b, "+") || strings.HasPrefix(b, "-") {
			differential = true
			break
		}
	}
	if differential {
		bats := sets.NewString(batteries.Defaults.List()...)
		for _, b := range o.Extra.BatteriesIncluded {
			if strings.HasPrefix(b, "+") {
				bats.Insert(b[1:])
			} else if strings.HasPrefix(b, "-") {
				bats.Delete(b[1:])
			}
		}
		o.Extra.BatteriesIncluded = bats.List()
	}

	completedEmbeddedEtcd := o.EmbeddedEtcd.Complete(o.GenericControlPlane.Etcd)
	cacheServerEtcdOptions := *o.GenericControlPlane.Etcd
	o.Cache.Server.Etcd = &cacheServerEtcdOptions
	// TODO: enable the watch cache, it was disabled because
	//  - we need to pass a shard name so that the watch cache can calculate the key
	//    we already do that for cluster names (stored in the obj)
	//  - we need to modify wildcardClusterNameRegex and crdWildcardPartialMetadataClusterNameRegex
	o.Cache.Server.Etcd.EnableWatchCache = false
	o.Cache.Server.SecureServing = completedGenericServerRunOptions.SecureServing
	cacheCompletedOptions, err := o.Cache.Complete()
	if err != nil {
		return nil, err
	}

	return &CompletedOptions{
		completedOptions: &completedOptions{
			// TODO: GenericControlPlane here should be completed. But the k/k repo does not expose the CompleteOptions type, but should.
			GenericControlPlane: completedGenericServerRunOptions,
			EmbeddedEtcd:        completedEmbeddedEtcd,
			Controllers:         o.Controllers,
			Authorization:       o.Authorization,
			AdminAuthentication: o.AdminAuthentication,
			Virtual:             o.Virtual,
			HomeWorkspaces:      o.HomeWorkspaces,
			Cache:               cacheCompletedOptions,
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

// mkdirRoot creates the root configuration directory for the KCP
// server. This has to be done early before we start bringing up server
// components to ensure that we set the initial permissions correctly,
// since otherwise components will create it as a side-effect.
func mkdirRoot(dir string) error {
	if dir == "" {
		return errors.New("missing root directory configuration")
	}
	logger := klog.Background().WithValues("dir", dir)

	fi, err := os.Stat(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}

		logger.Info("creating root directory")

		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}

		// Ensure the leaf directory is moderately private
		// because this may contain private keys and other
		// sensitive data
		return os.Chmod(dir, 0700)
	}

	if !fi.IsDir() {
		return fmt.Errorf("%q is a file, please delete or select another location", dir)
	}

	logger.Info("using root directory")
	return nil
}
