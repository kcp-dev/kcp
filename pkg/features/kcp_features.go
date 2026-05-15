/*
Copyright 2022 The kcp Authors.

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
	kubefeatures "k8s.io/kubernetes/pkg/features"
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

	// owner: @mjudeikis
	// alpha: v0.1
	// Enables cache apis and controllers.
	CacheAPIs featuregate.Feature = "CacheAPIs"

	// owner: @mjudeikis
	// alpha: v0.1
	// Enables VirtualWorkspace urls on APIExport. This enables to use Deprecated APIExport VirtualWorkspace urls.
	// This is a temporary feature to ease the migration to the new VirtualWorkspace urls.
	EnableDeprecatedAPIExportVirtualWorkspacesUrls featuregate.Feature = "EnableDeprecatedAPIExportVirtualWorkspacesUrls"

	// owner: @xrstf
	// alpha: v0.1
	// Enables per-workspace authentication using WorkspaceAuthenticationConfiguration objects in order to admit
	// users into workspaces from foreign OIDC issuers. This feature can be individually enabled on each shard and
	// the front-proxy.
	WorkspaceAuthentication featuregate.Feature = "WorkspaceAuthentication"
)

// Re-export upstream feature gates used by kcp server logic so callers import
// only this package instead of mixing kcp and upstream feature imports.
// Mirrored from k8s.io/apiserver/pkg/features.
const (
	// StorageVersionAPI enables the storage version API (alpha since k8s 1.20, off by default).
	// When enabled in kcp, WithStorageVersionPrecondition is applied in the handler chain to
	// block write requests to resources whose storage versions have not yet converged across
	// all kcp shards during a rolling upgrade.
	// See: https://github.com/kcp-dev/kubernetes/pull/185
	StorageVersionAPI = genericfeatures.StorageVersionAPI
)

// DefaultFeatureGate exposes the upstream feature gate, but with our gate setting applied.
var DefaultFeatureGate = utilfeature.DefaultFeatureGate

func init() {
	utilruntime.Must(utilfeature.DefaultMutableFeatureGate.AddVersioned(defaultVersionedGenericControlPlaneFeatureGates))

	utilruntime.Must(disableFeatures())
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
	pairs := make([]string, 0, len(defaultVersionedGenericControlPlaneFeatureGates))
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
	CacheAPIs: {
		{Version: version.MustParse("1.32"), Default: false, PreRelease: featuregate.Alpha},
	},
	EnableDeprecatedAPIExportVirtualWorkspacesUrls: {
		{Version: version.MustParse("1.32"), Default: false, PreRelease: featuregate.Alpha},
	},
	WorkspaceAuthentication: {
		{Version: version.MustParse("1.32"), Default: false, PreRelease: featuregate.Alpha},
	},
	// StorageVersionAPI mirrors the upstream k8s gate; kcp keeps it Alpha/off-by-default.
	// Must be listed here so kcp's feature gate machinery tracks it and exposes it via --feature-gates.
	genericfeatures.StorageVersionAPI: {
		{Version: version.MustParse("1.20"), Default: false, PreRelease: featuregate.Alpha},
	},
	// inherited features from generic apiserver, relisted here to get a conflict if it is changed
	// unintentionally on either side:
	genericfeatures.APIResponseCompression: {
		{Version: version.MustParse("1.8"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("1.16"), Default: true, PreRelease: featuregate.Beta},
	},

	genericfeatures.ConsistentListFromCache: {
		{Version: version.MustParse("1.28"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("1.31"), Default: true, PreRelease: featuregate.Beta},
		{Version: version.MustParse("1.34"), Default: true, PreRelease: featuregate.GA, LockToDefault: true},
	},

	genericfeatures.ListFromCacheSnapshot: {
		{Version: version.MustParse("1.33"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("1.34"), Default: true, PreRelease: featuregate.Beta},
	},

	genericfeatures.OpenAPIEnums: {
		{Version: version.MustParse("1.23"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("1.24"), Default: true, PreRelease: featuregate.Beta},
	},

	genericfeatures.StreamingCollectionEncodingToJSON: {
		{Version: version.MustParse("1.33"), Default: true, PreRelease: featuregate.Beta},
		{Version: version.MustParse("1.34"), Default: true, PreRelease: featuregate.GA, LockToDefault: true},
	},

	genericfeatures.StreamingCollectionEncodingToProtobuf: {
		{Version: version.MustParse("1.33"), Default: true, PreRelease: featuregate.Beta},
		{Version: version.MustParse("1.34"), Default: true, PreRelease: featuregate.GA, LockToDefault: true},
	},

	genericfeatures.WatchList: {
		{Version: version.MustParse("1.27"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("1.32"), Default: true, PreRelease: featuregate.Beta},
		{Version: version.MustParse("1.33"), Default: false, PreRelease: featuregate.Beta},
		{Version: version.MustParse("1.34"), Default: true, PreRelease: featuregate.Beta},
	},

	logsapi.LoggingBetaOptions: {
		{Version: version.MustParse("1.26"), Default: true, PreRelease: featuregate.Beta},
	},

	logsapi.ContextualLogging: {
		{Version: version.MustParse("1.26"), Default: true, PreRelease: featuregate.Alpha},
	},
}

// disableFeatures sets features that kcp wants disabled/enabled by default.
func disableFeatures() error {
	toDisable := map[featuregate.Feature]bool{
		// We disable SizeBasedListCostEstimate by default in kcp as stats collector does not have cluster awarness yet.
		// We add this here to track changes in future k8s releases.
		genericfeatures.SizeBasedListCostEstimate: false,

		// UnknownVersionInteroperabilityProxy causes the apiserver to
		// connect to etcd before starting up - including before
		// embeddedetcd is started. The feature allows an apiserver to
		// proxy to another apiserver in the fleet in case a request
		// cannot be handled due to the version not being known by the
		// apiserver.
		genericfeatures.UnknownVersionInteroperabilityProxy: false,

		// DynamicResourceAllocation is irrelevant in kcp.
		kubefeatures.DRAExtendedResource: false,

		// WatchCacheInitializationPostStartHook blocks /readyz until
		// all built-in APIs report ready. This isn't a problem for kcp
		// but causes the e2e home workspace tests to fail.
		// Disabling the feature gate for now and fixing the tests later.
		genericfeatures.WatchCacheInitializationPostStartHook: false,
	}
	for f, v := range toDisable {
		err := utilfeature.DefaultMutableFeatureGate.Set(fmt.Sprintf("%s=%v", f, v))
		if err != nil {
			return err
		}
	}
	return nil
}
