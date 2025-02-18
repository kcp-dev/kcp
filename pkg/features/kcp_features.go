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

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/version"
	genericfeatures "k8s.io/apiserver/pkg/features"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/component-base/featuregate"
	logsapi "k8s.io/component-base/logs/api/v1"
)

const (
	// Every feature gate should add method here following this template:
	//
	// // owner: @username
	// // alpha: v1.4
	// MyFeature() bool.

	// owner: @mjudeikis
	// alpha: v0.1
	// Enables workspace mounts via frontProxy.
	WorkspaceMounts featuregate.Feature = "WorkspaceMounts"
)

// DefaultFeatureGate exposes the upstream feature gate, but with our gate setting applied.
var DefaultFeatureGate = utilfeature.DefaultFeatureGate

func init() {
	utilruntime.Must(utilfeature.DefaultMutableFeatureGate.AddVersioned(defaultVersionedGenericControlPlaneFeatureGates))
}

func KnownFeatures() []string {
	features := make([]string, 0, len(defaultVersionedGenericControlPlaneFeatureGates))
	for k := range defaultVersionedGenericControlPlaneFeatureGates {
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

func featureSpecAtEmulationVersion(v featuregate.VersionedSpecs, emulationVersion *version.Version) *featuregate.FeatureSpec {
	i := len(v) - 1
	for ; i >= 0; i-- {
		if v[i].Version.GreaterThan(emulationVersion) {
			continue
		}
		return &v[i]
	}
	return &featuregate.FeatureSpec{
		Default:    false,
		PreRelease: featuregate.PreAlpha,
		Version:    version.MajorMinor(0, 0),
	}
}

func (f *kcpFeatureGate) String() string {
	pairs := []string{}
	emulatedVersion := utilfeature.DefaultMutableFeatureGate.EmulationVersion()

	for featureName, versionedSpecs := range defaultVersionedGenericControlPlaneFeatureGates {
		spec := featureSpecAtEmulationVersion(versionedSpecs, emulatedVersion)
		pairs = append(pairs, fmt.Sprintf("%s=%t", featureName, spec.Default))
	}

	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}

func (f *kcpFeatureGate) Type() string {
	return "mapStringBool"
}

// defaultGenericControlPlaneFeatureGates consists of all known Kubernetes-specific feature keys
// in the generic control plane code. To add a new feature, define a key for it above and add it
// here. The Version field should be set to whatever is specified in
// https://github.com/kubernetes/kubernetes/blob/master/pkg/features/versioned_kube_features.go.
// For features that are kcp-specific, the Version should be set to whatever go.mod k8s.io
// dependencies version we're currently using.
var defaultVersionedGenericControlPlaneFeatureGates = map[featuregate.Feature]featuregate.VersionedSpecs{
	WorkspaceMounts: {
		{Version: version.MustParse("1.28"), Default: false, PreRelease: featuregate.Alpha},
	},

	// inherited features from generic apiserver, relisted here to get a conflict if it is changed
	// unintentionally on either side:

	genericfeatures.APIResponseCompression: {
		{Version: version.MustParse("1.8"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("1.16"), Default: true, PreRelease: featuregate.Beta},
	},

	genericfeatures.OpenAPIEnums: {
		{Version: version.MustParse("1.23"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("1.24"), Default: true, PreRelease: featuregate.Beta},
	},

	logsapi.LoggingBetaOptions: {
		{Version: version.MustParse("1.26"), Default: true, PreRelease: featuregate.Beta},
	},

	logsapi.ContextualLogging: {
		{Version: version.MustParse("1.26"), Default: true, PreRelease: featuregate.Alpha},
	},

	genericfeatures.MutatingAdmissionPolicy: {
		{Version: version.MustParse("1.32"), Default: false, PreRelease: featuregate.Alpha},
	},
}
