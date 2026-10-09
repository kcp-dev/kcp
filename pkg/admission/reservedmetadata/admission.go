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

package reservedmetadata

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/handlers"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
)

// ReservedMetadata Plugin limits modifications and creation of labels
// and annotations using the kcp.io domain.
// Generally the plugin blocks any changes to kcp.io keys from non
// system privileged identities, with the exception of keys explicitly marked as unreserved.
const (
	PluginName = "apis.kcp.io/ReservedMetadata"
)

// reservation classifies a metadata key by who is allowed to modify it.
// higher tiers include all permissions from lower tiers.
type reservation int

const (
	// unreserved keys may be modified by anyone.
	unreserved reservation = iota
	// adminReserved keys may only be modified by kcp admins
	// (system:kcp:admin) and the control-plane system identities.
	adminReserved
	// systemReserved keys may only be modified by control-plane system identities.
	systemReserved
)

func (r reservation) String() string {
	switch r {
	case unreserved:
		return "unreserved"
	case adminReserved:
		return "admin-reserved"
	case systemReserved:
		return "system-reserved"
	default:
		return fmt.Sprintf("unknown reservation (%d)", r)
	}
}

type rule struct {
	matches     func(key string) bool
	reservation reservation
}

// exact returns a rule classifying exactly the given key.
func exact(r reservation, key string) rule {
	return rule{
		matches:     func(k string) bool { return k == key },
		reservation: r,
	}
}

// prefix returns a rule classifying all keys starting with the given prefix.
func prefix(r reservation, keyPrefix string) rule {
	return rule{
		matches:     func(k string) bool { return strings.HasPrefix(k, keyPrefix) },
		reservation: r,
	}
}

var (
	annotationRules = []rule{
		// storage layer annotations. Unfortunately these are also being written by
		// the following clients we are using:
		// * server bootstrap and bootstrap identity
		// * cache server replication
		exact(unreserved, logicalcluster.AnnotationKey),
		exact(unreserved, shard.AnnotationKey),
		exact(unreserved, handlers.KCPOriginalAPIVersionAnnotation),

		// pathAnnotation webhook sets this using user credentials
		exact(unreserved, core.LogicalClusterPathAnnotationKey),

		// the workspace mutating admission plugin sets these on Workspace objects
		// inside the user's own request, so they must be let through here. On
		// LogicalClusters they are guarded by the logicalcluster admission plugin.
		exact(unreserved, authorization.RequiredGroupsAnnotationKey),
		exact(unreserved, tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey),

		// workspace mount hook or WorkspaceType controller set these annotations.
		exact(unreserved, tenancyv1alpha1.ExperimentalWorkspaceMountAnnotationKey),
		exact(unreserved, tenancyv1alpha1.ExperimentalDefaultAPIBindingLifecycleAnnotationKey),

		// set on ResourceQuotas by workspace admin, not privileged group (see userClearance())
		exact(unreserved, "experimental.quota.kcp.io/cluster-scoped"),

		// set by APIExport owners (which don't have to be privileged users) directly on the object
		exact(unreserved, apisv1alpha2.APIExportEndpointSliceSkipAnnotation),

		// inactive needs to be let through here, since it is guarded by
		// logicalcluster admission plugin
		exact(unreserved, corev1alpha1.LogicalClusterInactiveAnnotationKey),

		// free-form annotations set by APIExport owners, synced to APIBindings
		prefix(unreserved, apisv1alpha1.AnnotationAPIExportExtraKeyPrefix),

		// v1alpha1<->v1alpha2 conversion round-trip annotations, which can use
		// user credentials
		prefix(unreserved, "apis.v1alpha2.kcp.io/"),

		// set manually by kcp admins on LogicalClusters, read by the
		// objectcountlimit admission plugin
		exact(adminReserved, corev1alpha1.LogicalClusterMaxTotalObjectsAnnotationKey),

		// set manually by kcp admins on Shards to exclude them from
		// workspace scheduling
		exact(adminReserved, corev1alpha1.ShardUnschedulableAnnotationKey),
	}

	labelRules = []rule{
		// stamped by the permissionclaims mutating admission plugin on claimed
		// objects inside the end user's own request;
		// the suffixes are hash generated, hence the prefix match
		prefix(unreserved, apisv1alpha1.APIExportPermissionClaimLabelPrefix),

		// set and validated by the ApiBinding admission plugin, so we need
		// to pass it here
		exact(unreserved, apisv1alpha1.InternalAPIBindingExportLabelKey),

		// currently used by our tests for marking objects;
		// These could potentially be moved out
		exact(unreserved, "internal.kcp.io/e2e-test"),
		exact(unreserved, "internal.kcp.io/test-initializer"),
	}
)

// Register registers the reserved metadata plugin for creation and updates.
// Deletion and connect operations are not relevant as not object changes are expected here.
func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &reservedMetadata{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}, nil
		})
}

// reservedMetadata is a validating admission plugin protecting against mutating reserved kcp metadata.
type reservedMetadata struct {
	*admission.Handler
}

var _ = admission.ValidationInterface(&reservedMetadata{})

// Validate asserts the underlying object for changes in labels and annotations to reserved kcp.io metadata.
// Every key is classified into a reservation tier and every user into a clearance on the same
// scale; a change is allowed if the user's clearance covers the key's reservation.
func (o *reservedMetadata) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	newMeta, err := meta.Accessor(a.GetObject())
	//nolint:nilerr
	if err != nil {
		// The object we are dealing with doesn't have object metadata defined
		// hence it doesn't have annotations to be checked.
		return nil
	}

	oldMeta, err := meta.Accessor(a.GetOldObject())
	if err != nil {
		oldMeta = &metav1.ObjectMeta{}
	}

	clearance := userClearance(a.GetUserInfo().GetGroups())
	if clearance == systemReserved {
		// the control plane may change any reserved metadata, so skip more expensive per-field diffs
		return nil
	}

	if k, res := reservedModification(newMeta.GetAnnotations(), oldMeta.GetAnnotations(), annotationRules, clearance); k != "" {
		return admission.NewForbidden(a, fmt.Errorf("modification of %s annotation: %q", res, k))
	}

	if k, res := reservedModification(newMeta.GetLabels(), oldMeta.GetLabels(), labelRules, clearance); k != "" {
		return admission.NewForbidden(a, fmt.Errorf("modification of %s label: %q", res, k))
	}

	return nil
}

// reservedModification returns the first changed key between old and new whose
// reservation exceeds the given clearance. An empty key means no clearance
// exceeding modification occurred.
func reservedModification(new, old map[string]string, rules []rule, clearance reservation) (key string, res reservation) {
	changed := func(k, v1, v2 string, v2present bool) (reservation, bool) {
		if v2present && v1 == v2 {
			return unreserved, false
		}
		if r := reservationOf(k, rules); r > clearance {
			return r, true
		}
		return unreserved, false
	}

	for k, v1 := range old {
		v2, ok := new[k]

		if r, bad := changed(k, v1, v2, ok); bad {
			return k, r
		}
	}

	for k, v1 := range new {
		v2, ok := old[k]

		if r, bad := changed(k, v1, v2, ok); bad {
			return k, r
		}
	}

	return "", unreserved
}

// reservationOf classifies a metadata key. Keys outside the kcp.io domain are
// never reserved; kcp.io-domain keys default to systemReserved unless a rule
// classifies them otherwise.
func reservationOf(key string, rules []rule) reservation {
	// exit early if the key is not kcp.io or a subdomain of it.
	// Doing this first saves us running through all the Regexes
	// for non reserved keys.
	domain, _, _ := strings.Cut(key, "/")
	if domain != "kcp.io" && !strings.HasSuffix(domain, ".kcp.io") {
		return unreserved
	}

	for _, r := range rules {
		if r.matches(key) {
			return r.reservation
		}
	}

	return systemReserved
}

// userClearance returns the highest reservation tier the user is allowed to modify.
// note: workspaceadmins are purposefully not considered to have any clearance for
// this plugin as this would lead to cross-workspace privileged escalations.
func userClearance(groups []string) reservation {
	switch {
	case slices.Contains(groups, user.SystemPrivilegedGroup),
		slices.Contains(groups, bootstrap.SystemLogicalClusterAdmin),
		slices.Contains(groups, bootstrap.SystemExternalLogicalClusterAdmin),
		slices.Contains(groups, bootstrap.SystemKcpWorkspaceBootstrapper):
		return systemReserved
	case slices.Contains(groups, bootstrap.SystemKcpAdminGroup):
		return adminReserved
	}

	return unreserved
}
