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

package admission

import (
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"
	kubeapiserveroptions "k8s.io/kubernetes/pkg/kubeapiserver/options"
)

func TestPluginDrift(t *testing.T) {
	kubeOffPlugins := kubeapiserveroptions.DefaultOffAdmissionPlugins()
	kubeOnPlugins := sets.New[string](kubeapiserveroptions.AllOrderedPlugins...).Difference(kubeOffPlugins)

	if newInKube := kubeOnPlugins.Difference(defaultOnKubePluginsInKube); newInKube.Len() > 0 {
		t.Errorf("Default-on plugins got added in kube. Add to defaultOnKubePluginsInKube, and decide whether to add to defaultOnPluginsInKcp: %v", sets.List[string](newInKube))
	}
	if goneInKube := defaultOnKubePluginsInKube.Difference(kubeOnPlugins); goneInKube.Len() > 0 {
		t.Errorf("Default-on plugins got removed in kube. Remove in defaultOnKubePluginsInKube, and decide whether to remove from defaultOnPluginsInKcp: %v", sets.List[string](goneInKube))
	}
}
