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
	"regexp"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

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
	pattern     *regexp.Regexp
	reservation reservation
}

var (
	annotationRules = []rule{
		// storage layer annotations. Unfortunately these are also being written by
		// the following clients we are using:
		// * server bootstrap and bootstrap identity
		// * cache server replication
		{regexp.MustCompile(`^kcp\.io/(cluster|shard|original-api-version)$`), unreserved},

		// pathAnnotation webhook sets this using user credentials
		{regexp.MustCompile(`^kcp\.io/path$`), unreserved},

		// workspace mutating webhook sets these on logicalclusters using user credentials
		{regexp.MustCompile(`^authorization\.kcp\.io/required-groups$`), unreserved},
		{regexp.MustCompile(`^experimental\.tenancy\.kcp\.io/owner$`), unreserved},

		// note: combined together to reduce number of individual regexps
		// workspace mount hook or WorkspaceType controller set these annotations.
		{regexp.MustCompile(`^experimental\.tenancy\.kcp\.io/(mount|default-api-binding-lifecycle)$`), unreserved},

		// set on ResourceQuotas by workspace admin, not privileged group (see userClearance())
		{regexp.MustCompile(`^experimental\.quota\.kcp\.io/cluster-scoped$`), unreserved},

		// set by APIExport owners (which don't have to be privileged users) directly on the object
		{regexp.MustCompile(`^apiexports\.apis\.kcp\.io/skip-endpointslice$`), unreserved},

		// inactive needs to be let through here, since it is guarded by
		// logicalcluster admission plugin
		{regexp.MustCompile(`^core\.kcp\.io/(inactive)$`), unreserved},

		// free-form annotations set by APIExport owners, synced to APIBindings
		{regexp.MustCompile(`^extra\.apis\.kcp\.io/`), unreserved},

		// v1alpha1<->v1alpha2 conversion round-trip annotations, which can use
		// user credentials
		{regexp.MustCompile(`^apis\.v1alpha2\.kcp\.io/`), unreserved},

		// set manually by kcp admins on LogicalClusters, read by the
		// objectcountlimit admission plugin
		{regexp.MustCompile(`^core\.kcp\.io/max-total-objects$`), adminReserved},

		// set manually by kcp admins on Shards to exclude them from
		// workspace scheduling
		{regexp.MustCompile(`^experimental\.core\.kcp\.io/unschedulable$`), adminReserved},
	}

	labelRules = []rule{
		// stamped by the permissionclaims mutating admission plugin on claimed
		// objects inside the end user's own request
		// we need to match the full suffix here as they are hash generated
		{regexp.MustCompile(`^claimed\.internal\.apis\.kcp\.io/`), unreserved},

		// set and validated by the ApiBinding admission plugin, so we need
		// to pass it here
		{regexp.MustCompile(`^internal\.apis\.kcp\.io/export$`), unreserved},

		// currently used by our tests for marking objects;
		// These could potentially be moved out
		{regexp.MustCompile(`^internal\.kcp\.io/(e2e-test|test-initializer)$`), unreserved},
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
		if r.pattern.MatchString(key) {
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
