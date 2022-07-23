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

package features

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/pflag"

	"k8s.io/apimachinery/pkg/util/runtime"
	genericfeatures "k8s.io/apiserver/pkg/features"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/component-base/featuregate"
	"k8s.io/component-base/logs"
)

const (
	// Every feature gate should add method here following this template:
	//
	// // owner: @username
	// // alpha: v1.4
	// MyFeature() bool

	// owner: @sttts
	// alpha: v0.4
	//
	// Enable the scheduling.kcp.dev/v1alpha1 API group, and related controllers.
	LocationAPI featuregate.Feature = "KCPLocationAPI"
)

// DefaultFeatureGate exposes the upstream feature gate, but with our gate setting applied.
var DefaultFeatureGate = utilfeature.DefaultFeatureGate

func init() {
	runtime.Must(utilfeature.DefaultMutableFeatureGate.Add(defaultGenericControlPlaneFeatureGates))

	// here we differ from upstream:
	runtime.Must(utilfeature.DefaultMutableFeatureGate.Set(fmt.Sprintf("%s=true", genericfeatures.CustomResourceValidationExpressions)))
}

func KnownFeatures() []string {
	var features []string
	for k := range defaultGenericControlPlaneFeatureGates {
		features = append(features, string(k))
	}
	return features
}

// NewFlagValue returns a wrapper to be used for a pflag flag value.
func NewFlagValue() pflag.Value {
	return &kcpFeatureGate{
		utilfeature.DefaultMutableFeatureGate,
	}
}

type kcpFeatureGate struct {
	featuregate.MutableFeatureGate
}

func (f *kcpFeatureGate) String() string {
	pairs := []string{}
	for k, v := range defaultGenericControlPlaneFeatureGates {
		pairs = append(pairs, fmt.Sprintf("%s=%t", k, v.Default))
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}

func (f *kcpFeatureGate) Type() string {
	return "mapStringBool"
}

// defaultGenericControlPlaneFeatureGates consists of all known Kubernetes-specific feature keys
// in the generic control plane code. To add a new feature, define a key for it above and add it
// here. The features will be available throughout Kubernetes binaries.
var defaultGenericControlPlaneFeatureGates = map[featuregate.Feature]featuregate.FeatureSpec{
	LocationAPI: {Default: true, PreRelease: featuregate.Alpha},

	// inherited features from generic apiserver, relisted here to get a conflict if it is changed
	// unintentionally on either side:
	genericfeatures.AdvancedAuditing:                    {Default: true, PreRelease: featuregate.GA},
	genericfeatures.APIResponseCompression:              {Default: true, PreRelease: featuregate.Beta},
	genericfeatures.APIListChunking:                     {Default: true, PreRelease: featuregate.Beta},
	genericfeatures.DryRun:                              {Default: true, PreRelease: featuregate.GA},
	genericfeatures.ServerSideApply:                     {Default: true, PreRelease: featuregate.GA},
	genericfeatures.APIPriorityAndFairness:              {Default: true, PreRelease: featuregate.Beta},
	genericfeatures.CustomResourceValidationExpressions: {Default: false, PreRelease: featuregate.Alpha},

	logs.ContextualLogging: {Default: true, PreRelease: featuregate.Alpha},
}
