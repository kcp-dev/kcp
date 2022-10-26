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

package framework

import (
	"testing"

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type WorkloadBindOption func(t *testing.T, w *workloadBind)

// workloadBind construct and run a kcp workload bind command
type workloadBind struct {
	apiExports        []string
	nsSelector        string
	locationSelectors []string
	computeWorkspace  logicalcluster.Name
	kubeconfigPath    string
}

func NewWorkloadBind(t *testing.T, clusterName logicalcluster.Name, server RunningServer, opts ...WorkloadBindOption) *workloadBind {
	upstreamRawConfig, err := server.RawConfig()
	require.NoError(t, err)

	_, kubeconfigPath := WriteLogicalClusterConfig(t, upstreamRawConfig, "base", clusterName)

	workloadBind := &workloadBind{
		kubeconfigPath:   kubeconfigPath,
		computeWorkspace: clusterName,
	}

	for _, opt := range opts {
		opt(t, workloadBind)
	}

	return workloadBind
}

func (w workloadBind) Bind(t *testing.T) {
	t.Logf("Bind workload workspace %s", w.computeWorkspace)
	pluginArgs := []string{
		"bind",
		"workload",
		w.computeWorkspace.String(),
	}

	if len(w.nsSelector) > 0 {
		pluginArgs = append(pluginArgs, "--namespace-selector="+w.nsSelector)
	}

	for _, apiexport := range w.apiExports {
		pluginArgs = append(pluginArgs, "--apiexports="+apiexport)
	}

	for _, locationSelector := range w.locationSelectors {
		pluginArgs = append(pluginArgs, "--location-selectors="+locationSelector)
	}

	RunKcpCliPlugin(t, w.kubeconfigPath, pluginArgs)
}

func WithComputeWorkspaceWorkloadBindOption(clusterName logicalcluster.Name) WorkloadBindOption {
	return func(t *testing.T, w *workloadBind) {
		w.computeWorkspace = clusterName
	}
}

func WithAPIExportsWorkloadBindOption(apiexports ...string) WorkloadBindOption {
	return func(t *testing.T, w *workloadBind) {
		w.apiExports = apiexports
	}
}

func WithNSSelectorWorkloadBindOption(selector metav1.LabelSelector) WorkloadBindOption {
	return func(t *testing.T, w *workloadBind) {
		labelSelector, err := metav1.LabelSelectorAsSelector(&selector)
		require.NoError(t, err)
		w.nsSelector = labelSelector.String()
	}
}

func WithLocationSelectorWorkloadBindOption(selectors ...metav1.LabelSelector) WorkloadBindOption {
	return func(t *testing.T, w *workloadBind) {
		for _, selector := range selectors {
			labelSelector, err := metav1.LabelSelectorAsSelector(&selector)
			require.NoError(t, err)
			w.locationSelectors = append(w.locationSelectors, labelSelector.String())
		}
	}
}
