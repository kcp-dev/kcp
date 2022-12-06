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

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/spf13/pflag"

	"k8s.io/apiserver/pkg/authentication/user"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

type HomeWorkspaces struct {
	Enabled bool

	CreationDelaySeconds int
	BucketLevels         int
	BucketSize           int

	HomeCreatorGroups []string
	HomeRootPrefix    string
}

func NewHomeWorkspaces() *HomeWorkspaces {
	return &HomeWorkspaces{
		Enabled:              true,
		CreationDelaySeconds: 2,
		BucketLevels:         2,
		BucketSize:           2,
		HomeCreatorGroups:    []string{user.AllAuthenticated},
		HomeRootPrefix:       "root:users",
	}
}

func (hw *HomeWorkspaces) AddFlags(fs *pflag.FlagSet) {
	fs.BoolVar(&hw.Enabled, "enable-home-workspaces", hw.Enabled, "Enable the Home Workspaces feature. Home workspaces allow a personal home workspace to provisioned on first access per-user. A user is cluster-admin inside his personal Home workspace.")
	fs.IntVar(&hw.CreationDelaySeconds, "home-workspaces-creation-delay-seconds", hw.CreationDelaySeconds, "Delay, in seconds, before retrying accessing the Home workspace after its automatic creation. This value is used when sending 'retry-after' responses to the Kubernetes client.")
	fs.IntVar(&hw.BucketLevels, "home-workspaces-bucket-levels", hw.BucketLevels, "Number of levels of bucket workspaces when bucketing home workspaces")
	fs.IntVar(&hw.BucketLevels, "home-workspaces-bucket-size", hw.BucketSize, "Number of characters of bucket workspace names used when bucketing home workspaces")
	fs.StringSliceVar(&hw.HomeCreatorGroups, "home-workspaces-home-creator-groups", hw.HomeCreatorGroups, "Groups of users who can have their home workspace created automatically create when first accessing it.")
	fs.StringVar(&hw.HomeRootPrefix, "home-workspaces-root-prefix", hw.HomeRootPrefix, "Logical cluster name of the workspace that will contains home workspaces for all workspaces.")

	fs.MarkDeprecated("home-workspaces-home-creator-groups", "This flag is deprecated and will be removed in a future release.")    //nolint:errcheck
	fs.MarkDeprecated("home-workspaces-root-prefix", "This flag is deprecated and will be removed in a future release.")            //nolint:errcheck
	fs.MarkDeprecated("home-workspaces-creation-delay-seconds", "This flag is deprecated and will be removed in a future release.") //nolint:errcheck
	fs.MarkDeprecated("home-workspaces-bucket-levels", "This flag is deprecated and will be removed in a future release.")          //nolint:errcheck
	fs.MarkDeprecated("home-workspaces-bucket-size", "This flag is deprecated and will be removed in a future release.")            //nolint:errcheck
}

func (e *HomeWorkspaces) Validate() []error {
	var errs []error

	if e.Enabled {
		if e.BucketLevels < 1 || e.BucketLevels > 5 {
			errs = append(errs, fmt.Errorf("--home-workspaces-bucket-levels should be between 1 and 5"))
		}
		if e.BucketSize < 1 || e.BucketLevels > 4 {
			errs = append(errs, fmt.Errorf("--home-workspaces-bucket-size should be between 1 and 4"))
		}
		if e.CreationDelaySeconds < 1 {
			errs = append(errs, fmt.Errorf("--home-workspaces-creation-delay-seconds should be between 1"))
		}
		if homePrefix := logicalcluster.New(e.HomeRootPrefix); !homePrefix.IsValid() ||
			homePrefix == logicalcluster.Wildcard ||
			!homePrefix.HasPrefix(tenancyv1alpha1.RootCluster.Path()) {
			errs = append(errs, fmt.Errorf("--home-workspaces-root-prefix should be a valid logical cluster name"))
		} else if parent, ok := homePrefix.Parent(); !ok || parent != tenancyv1alpha1.RootCluster.Path() {
			errs = append(errs, fmt.Errorf("--home-workspaces-root-prefix should be a direct child of the root logical cluster"))
		}
	}

	return errs
}
