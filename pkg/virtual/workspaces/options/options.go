/*
Copyright 2021 The KCP Authors.

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
	"path"
	"time"

	"github.com/spf13/pflag"

	"k8s.io/client-go/rest"

	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	"github.com/kcp-dev/kcp/pkg/virtual/workspaces/builder"
)

type Workspaces struct {
	AuthorizationCache AuthorizationCache
}

// AuthorizationCache contains options for the authorization caches in the workspaces service.
type AuthorizationCache struct {
	Period       time.Duration
	JitterFactor float64
	Sliding      bool
}

func New() *Workspaces {
	return &Workspaces{
		AuthorizationCache: AuthorizationCache{
			Period:       1 * time.Minute,
			JitterFactor: 0,    // use the default
			Sliding:      true, // take into account processing time
		},
	}
}

const workspacesPrefix = "workspaces."
const authorizationCachePrefix = "authorization-cache."

func (o *Workspaces) AddFlags(flags *pflag.FlagSet, prefix string) {
	if o == nil {
		return
	}
	o.AuthorizationCache.AddFlags(flags, prefix+workspacesPrefix+authorizationCachePrefix)
}

func (o *AuthorizationCache) AddFlags(flags *pflag.FlagSet, prefix string) {
	if o == nil {
		return
	}
	flags.DurationVar(&o.Period, prefix+"resync-period", o.Period, "Period for cache re-sync.")
	flags.Float64Var(&o.JitterFactor, prefix+"jitter-factor", o.JitterFactor, "Jitter factor for cache re-sync. Leave unset to use a default factor.")
	flags.BoolVar(&o.Sliding, prefix+"sliding", o.Sliding, "Whether or not to take into account sync duration in period calculations.")
}

func (o *Workspaces) Validate(flagPrefix string) []error {
	if o == nil {
		return nil
	}
	errs := []error{}
	errs = append(errs, o.AuthorizationCache.Validate(flagPrefix+workspacesPrefix+authorizationCachePrefix)...)

	return errs
}

func (o *AuthorizationCache) Validate(flagPrefix string) []error {
	var errs []error
	if o.Period == 0 {
		errs = append(errs, fmt.Errorf("--%sresync-period cannot be 0", flagPrefix))
	}
	if o.JitterFactor < 0 {
		errs = append(errs, fmt.Errorf("--%sjitter-factor cannot be less than 0", flagPrefix))
	}
	return errs
}

func (o *Workspaces) NewVirtualWorkspaces(
	rootPathPrefix string,
	config *rest.Config,
) (workspaces []rootapiserver.NamedVirtualWorkspace, err error) {
	config = rest.AddUserAgent(rest.CopyConfig(config), "clusterworkspaces-virtual-workspace")
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return []rootapiserver.NamedVirtualWorkspace{
		{Name: "clusterworkspaces", VirtualWorkspace: builder.BuildVirtualWorkspace(config, path.Join(rootPathPrefix, "clusterworkspaces"), kcpClusterClient)},
	}, nil
}
