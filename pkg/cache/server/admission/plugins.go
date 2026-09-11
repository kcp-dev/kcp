/*
Copyright 2025 The kcp Authors.

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
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
	admissionmetrics "k8s.io/apiserver/pkg/admission/metrics"
	genericoptions "k8s.io/apiserver/pkg/server/options"

	"github.com/kcp-dev/kcp/pkg/cache/server/admission/cacheannotation"
)

// AllOrderedPlugins is the list of all cache-server admission plugins in order.
var AllOrderedPlugins = []string{
	cacheannotation.PluginName,
}

// RegisterAllCacheServerAdmissionPlugins registers all cache-server admission plugins.
// The order of registration is irrelevant, see AllOrderedPlugins for execution order.
func RegisterAllCacheServerAdmissionPlugins(plugins *admission.Plugins) {
	cacheannotation.Register(plugins)
}

var defaultOnPlugins = sets.New[string](
	cacheannotation.PluginName,
)

// DefaultOffAdmissionPlugins returns the set of cache-server admission plugins that are off by default.
func DefaultOffAdmissionPlugins() sets.Set[string] {
	return sets.New[string](AllOrderedPlugins...).Difference(defaultOnPlugins)
}

// NewAdmissionOptions returns admission options pre-configured for the cache server.
// It uses a fresh plugin registry containing only cache-server plugins, so the
// RecommendedPluginOrder and DefaultOffPlugins are consistent with that set.
func NewAdmissionOptions() *genericoptions.AdmissionOptions {
	options := &genericoptions.AdmissionOptions{
		Plugins:                admission.NewPlugins(),
		Decorators:             admission.Decorators{admission.DecoratorFunc(admissionmetrics.WithControllerMetrics)},
		RecommendedPluginOrder: AllOrderedPlugins,
		DefaultOffPlugins:      DefaultOffAdmissionPlugins(),
	}
	RegisterAllCacheServerAdmissionPlugins(options.Plugins)
	return options
}
