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
	"strings"
	"time"

	"github.com/spf13/pflag"

	"k8s.io/component-base/logs"
	logsapiv1 "k8s.io/component-base/logs/api/v1"

	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	workloadv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
)

type Options struct {
	QPS                           float32
	Burst                         int
	FromKubeconfig                string
	FromContext                   string
	FromClusterPath               string
	ToKubeconfig                  string
	ToContext                     string
	SyncTargetName                string
	SyncTargetUID                 string
	Logs                          *logs.Options
	SyncedResourceTypes           []string
	DNSImage                      string
	DownstreamNamespaceCleanDelay time.Duration

	APIImportPollInterval time.Duration
}

func NewOptions() *Options {
	// Default to -v=2
	logsOptions := logs.NewOptions()
	logsOptions.Verbosity = logsapiv1.VerbosityLevel(2)

	return &Options{
		QPS:                           30,
		Burst:                         20,
		SyncedResourceTypes:           []string{},
		Logs:                          logsOptions,
		APIImportPollInterval:         1 * time.Minute,
		DownstreamNamespaceCleanDelay: 30 * time.Second,
	}
}

func (options *Options) AddFlags(fs *pflag.FlagSet) {
	fs.Float32Var(&options.QPS, "qps", options.QPS, "QPS to use when talking to API servers.")
	fs.IntVar(&options.Burst, "burst", options.Burst, "Burst to use when talking to API servers.")
	fs.StringVar(&options.FromKubeconfig, "from-kubeconfig", options.FromKubeconfig, "Kubeconfig file for -from cluster.")
	fs.StringVar(&options.FromContext, "from-context", options.FromContext, "Context to use in the Kubeconfig file for -from cluster, instead of the current context.")
	fs.StringVar(&options.FromClusterPath, "from-cluster", options.FromClusterPath, "Path of the -from logical cluster.")
	fs.StringVar(&options.ToKubeconfig, "to-kubeconfig", options.ToKubeconfig, "Kubeconfig file for -to cluster. If not set, the InCluster configuration will be used.")
	fs.StringVar(&options.ToContext, "to-context", options.ToContext, "Context to use in the Kubeconfig file for -to cluster, instead of the current context.")
	fs.StringVar(&options.SyncTargetName, "sync-target-name", options.SyncTargetName,
		fmt.Sprintf("ID of the -to cluster. Resources with this ID set in the '%s' label will be synced.", workloadv1alpha1.ClusterResourceStateLabelPrefix+"<ClusterID>"))
	fs.StringVar(&options.SyncTargetUID, "sync-target-uid", options.SyncTargetUID, "The UID from the SyncTarget resource in KCP.")
	fs.StringArrayVarP(&options.SyncedResourceTypes, "resources", "r", options.SyncedResourceTypes, "Resources to be synchronized in kcp.")
	fs.DurationVar(&options.APIImportPollInterval, "api-import-poll-interval", options.APIImportPollInterval, "Polling interval for API import.")
	fs.Var(kcpfeatures.NewFlagValue(), "feature-gates", ""+
		"A set of key=value pairs that describe feature gates for alpha/experimental features. "+
		"Options are:\n"+strings.Join(kcpfeatures.KnownFeatures(), "\n")) // hide kube-only gates
	fs.StringVar(&options.DNSImage, "dns-image", options.DNSImage, "kcp DNS server image.")
	fs.DurationVar(&options.DownstreamNamespaceCleanDelay, "downstream-namespace-clean-delay", options.DownstreamNamespaceCleanDelay, "Time to wait before deleting a downstream namespace, defaults to 30s.")

	logsapiv1.AddFlags(options.Logs, fs)
}

func (options *Options) Complete() error {
	return nil
}

func (options *Options) Validate() error {
	if options.FromClusterPath == "" {
		return errors.New("--from-cluster is required")
	}
	if options.FromKubeconfig == "" {
		return errors.New("--from-kubeconfig is required")
	}
	if options.SyncTargetUID == "" {
		return errors.New("--sync-target-uid is required")
	}
	return nil
}
