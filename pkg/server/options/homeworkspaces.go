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
	"github.com/spf13/pflag"

	"k8s.io/apiserver/pkg/authentication/user"
)

type HomeWorkspaces struct {
	Enabled           bool
	HomeCreatorGroups []string
}

func NewHomeWorkspaces() *HomeWorkspaces {
	return &HomeWorkspaces{
		Enabled:           true,
		HomeCreatorGroups: []string{user.AllAuthenticated},
	}
}

func (hw *HomeWorkspaces) AddFlags(fs *pflag.FlagSet) {
	fs.BoolVar(&hw.Enabled, "enable-home-workspaces", hw.Enabled, "Enable home workspaces, where a private workspace is provisioned upon first access per user, and the user is cluster-admin.")
	fs.StringSliceVar(&hw.HomeCreatorGroups, "home-workspaces-home-creator-groups", hw.HomeCreatorGroups, "Groups of users who can have their home workspaces provisioned upon first access.")
}

func (hw *HomeWorkspaces) Validate() []error {
	return nil
}
