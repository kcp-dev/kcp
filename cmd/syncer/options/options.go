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

	"github.com/spf13/pflag"

	"k8s.io/component-base/logs"

	nscontroller "github.com/kcp-dev/kcp/pkg/reconciler/workload/namespace"
)

type Options struct {
	FromKubeconfig      string
	FromClusterName     string
	ToKubeconfig        string
	ToContext           string
	PclusterID          string
	Logs                *logs.Options
	SyncedResourceTypes []string
}

func NewOptions() *Options {
	return &Options{
		SyncedResourceTypes: []string{},
		Logs:                logs.NewOptions(),
	}
}

func (options *Options) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&options.FromKubeconfig, "from-kubeconfig", options.FromKubeconfig, "Kubeconfig file for -from cluster.")
	fs.StringVar(&options.FromClusterName, "from-cluster", options.FromClusterName, "Name of the -from logical cluster.")
	fs.StringVar(&options.ToKubeconfig, "to-kubeconfig", options.ToKubeconfig, "Kubeconfig file for -to cluster. If not set, the InCluster configuration will be used.")
	fs.StringVar(&options.ToContext, "to-context", options.ToContext, "Context to use in the Kubeconfig file for -to cluster, instead of the current context.")
	fs.StringVar(&options.PclusterID, "workload-cluster-name", options.PclusterID,
		fmt.Sprintf("ID of the -to cluster. Resources with this ID set in the '%s' label will be synced.", nscontroller.ClusterLabel))
	fs.StringArrayVarP(&options.SyncedResourceTypes, "sync-resources", "r", options.SyncedResourceTypes, "Resources to be synchronized in kcp.")

	options.Logs.AddFlags(fs)
}

func (options *Options) Complete() error {
	return nil
}

func (options *Options) Validate() error {
	if options.FromClusterName == "" {
		return errors.New("--from-cluster is required")
	}
	if options.FromKubeconfig == "" {
		return errors.New("--from-kubeconfig is required")
	}

	return nil
}
