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

package syncer

import (
	"errors"

	"github.com/spf13/pflag"
)

// DefaultOptions are the default options for the cluster controller.
func DefaultOptions() *Options {
	return &Options{
		SyncerImage:     "",
		PullMode:        false,
		PushMode:        false,
		ResourcesToSync: []string{"deployments.apps"},
	}
}

// BindOptions binds the cluster controller options to the flag set.
func BindOptions(o *Options, fs *pflag.FlagSet) *Options {
	fs.StringVar(&o.SyncerImage, "syncer-image", o.SyncerImage, "Syncer image to install on clusters")
	fs.BoolVar(&o.PullMode, "pull-mode", o.PullMode, "Deploy the syncer in registered physical clusters in POD, and have it sync resources from KCP")
	fs.BoolVar(&o.PushMode, "push-mode", o.PushMode, "If true, run syncer for each cluster from inside cluster controller")
	// TODO(marun) --resources-to-sync is currently defined in options for the api importer
	// controller. How to best to define the option once for reuse by both api importer and
	// sync controllers?
	return o
}

// Options are the options for the cluster controller
type Options struct {
	SyncerImage     string
	PullMode        bool
	PushMode        bool
	ResourcesToSync []string
}

func (o *Options) Validate() error {
	if o.PullMode && o.PushMode {
		return errors.New("can't set both --push-mode and --pull-mode")
	}
	return nil
}

func (o *Options) CreateSyncerManager(externalAddress string) SyncerManager {
	if o.PullMode {
		return newPullSyncerManager(o.SyncerImage)
	} else if o.PushMode {
		return newPushSyncerManager(externalAddress)
	}

	// No mode, no controller required
	return nil
}
