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

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type BindComputeOption func(t *testing.T, w *bindCompute)

// bindCompute construct and run a kcp compute bind command
type bindCompute struct {
	apiExports        []string
	nsSelector        string
	locationSelectors []string
	locationWorkspace logicalcluster.Name
	kubeconfigPath    string
	placementName     string
}

func NewBindCompute(t *testing.T, clusterName logicalcluster.Name, server RunningServer, opts ...BindComputeOption) *bindCompute {
	upstreamRawConfig, err := server.RawConfig()
	require.NoError(t, err)

	_, kubeconfigPath := WriteLogicalClusterConfig(t, upstreamRawConfig, "base", clusterName)

	workloadBind := &bindCompute{
		kubeconfigPath:    kubeconfigPath,
		locationWorkspace: clusterName,
	}

	for _, opt := range opts {
		opt(t, workloadBind)
	}

	return workloadBind
}

func (w bindCompute) Bind(t *testing.T) {
	t.Logf("Bind workload workspace %s", w.locationWorkspace)
	pluginArgs := []string{
		"bind",
		"compute",
		w.locationWorkspace.String(),
	}

	if len(w.nsSelector) > 0 {
		pluginArgs = append(pluginArgs, "--namespace-selector="+w.nsSelector)
	}

	if len(w.placementName) > 0 {
		pluginArgs = append(pluginArgs, "--name="+w.placementName)
	}

	for _, apiexport := range w.apiExports {
		pluginArgs = append(pluginArgs, "--apiexports="+apiexport)
	}

	for _, locationSelector := range w.locationSelectors {
		pluginArgs = append(pluginArgs, "--location-selectors="+locationSelector)
	}

	RunKcpCliPlugin(t, w.kubeconfigPath, pluginArgs)
}

func WithPlacementNameBindOption(placementName string) BindComputeOption {
	return func(t *testing.T, w *bindCompute) {
		w.placementName = placementName
	}
}

func WithLocationWorkspaceWorkloadBindOption(clusterName logicalcluster.Name) BindComputeOption {
	return func(t *testing.T, w *bindCompute) {
		w.locationWorkspace = clusterName
	}
}

func WithAPIExportsWorkloadBindOption(apiexports ...string) BindComputeOption {
	return func(t *testing.T, w *bindCompute) {
		w.apiExports = apiexports
	}
}

func WithNSSelectorWorkloadBindOption(selector metav1.LabelSelector) BindComputeOption {
	return func(t *testing.T, w *bindCompute) {
		labelSelector, err := metav1.LabelSelectorAsSelector(&selector)
		require.NoError(t, err)
		w.nsSelector = labelSelector.String()
	}
}

func WithLocationSelectorWorkloadBindOption(selectors ...metav1.LabelSelector) BindComputeOption {
	return func(t *testing.T, w *bindCompute) {
		for _, selector := range selectors {
			labelSelector, err := metav1.LabelSelectorAsSelector(&selector)
			require.NoError(t, err)
			w.locationSelectors = append(w.locationSelectors, labelSelector.String())
		}
	}
}
