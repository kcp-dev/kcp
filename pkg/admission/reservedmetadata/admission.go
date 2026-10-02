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

var (
	annotationAllowList = []*regexp.Regexp{
		// storage layer annotations. Unfortunately these are also being written by
		// the following clients we are using:
		// * server bootstrap and bootstrap identity
		// * cache server replication
		regexp.MustCompile(`^kcp\.io/(cluster|shard|original-api-version)$`),

		// pathAnnotation webhook sets this using user credentials
		regexp.MustCompile(`^kcp\.io/path$`),

		// workspace mutating webhook sets these on logicalclusters using user credentials
		regexp.MustCompile(`^authorization\.kcp\.io/required-groups$`),
		regexp.MustCompile(`^experimental\.tenancy\.kcp\.io/owner$`),

		// note: combined together to reduce number of individual regexps
		// workspace mount hook or WorkspaceType controller set these annotations.
		regexp.MustCompile(`^experimental\.tenancy\.kcp\.io/(mount|default-api-binding-lifecycle)$`),

		// set on ResourceQuotas by workspace admin, not privileged group (see isPrivilegedUser())
		regexp.MustCompile(`^experimental\.quota\.kcp\.io/cluster-scoped$`),

		// set by APIExport owners (which don't have to be privileged users) directly on the object
		regexp.MustCompile(`^apiexports\.apis\.kcp\.io/skip-endpointslice$`),

		// note: combined together to reduce number of individual regexps
		// * max-total-objects is being set by workspace admin directly
		// * inactive needs to be let through here, since it is guarded by
		//   logicalcluster admission plugin
		regexp.MustCompile(`^core\.kcp\.io/(max-total-objects|inactive)$`),

		// free-form annotations set by APIExport owners, synced to APIBindings
		regexp.MustCompile(`^extra\.apis\.kcp\.io/`),

		// v1alpha1<->v1alpha2 conversion round-trip annotations, which can use
		// user credentials
		regexp.MustCompile(`^apis\.v1alpha2\.kcp\.io/`),
	}

	labelAllowList = []*regexp.Regexp{
		// stamped by the permissionclaims mutating admission plugin on claimed
		// objects inside the end user's own request
		// we need to match the full suffix here as they are hash generated
		regexp.MustCompile(`^claimed\.internal\.apis\.kcp\.io/`),

		// set and validated by the ApiBinding admission plugin, so we need
		// to pass it here
		regexp.MustCompile(`^internal\.apis\.kcp\.io/export$`),

		// currently used by our tests for marking objects;
		// These could potentially be moved out
		regexp.MustCompile(`^internal\.kcp\.io/(e2e-test|test-initializer)$`),
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
// If the user is member of the privileged system group, all mutations are allowed.
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

	// allow privileged users to change any reserved metadata
	if isPrivilegedUser(a.GetUserInfo().GetGroups()) {
		return nil
	}

	if k, ok := hasPrivilegedModification(newMeta.GetAnnotations(), oldMeta.GetAnnotations(), annotationAllowList); ok {
		return admission.NewForbidden(a, fmt.Errorf("modification of reserved annotation: %q", k))
	}

	if k, ok := hasPrivilegedModification(newMeta.GetLabels(), oldMeta.GetLabels(), labelAllowList); ok {
		return admission.NewForbidden(a, fmt.Errorf("modification of reserved label: %q", k))
	}

	return nil
}

func hasPrivilegedModification(new, old map[string]string, allowList []*regexp.Regexp) (key string, modified bool) {
	hasChanged := func(k, v1, v2 string, v2present bool) bool {
		return (!v2present || v1 != v2) && isPrivileged(k, allowList)
	}

	for k, v1 := range old {
		v2, ok := new[k]

		if hasChanged(k, v1, v2, ok) {
			return k, true
		}
	}

	for k, v1 := range new {
		v2, ok := old[k]

		if hasChanged(k, v1, v2, ok) {
			return k, true
		}
	}

	return "", false
}

func isPrivileged(key string, allowList []*regexp.Regexp) bool {
	// exit early if the key is not kcp.io or a subdomain of it.
	// Doing this first saves us running through all the Regexes
	// for non privileged keys.
	domain, _, _ := strings.Cut(key, "/")
	if domain != "kcp.io" && !strings.HasSuffix(domain, ".kcp.io") {
		return false
	}

	for _, re := range allowList {
		if re.MatchString(key) {
			return false
		}
	}

	return true
}

func isPrivilegedUser(groups []string) bool {
	return slices.Contains(groups, user.SystemPrivilegedGroup) ||
		slices.Contains(groups, bootstrap.SystemLogicalClusterAdmin) ||
		slices.Contains(groups, bootstrap.SystemExternalLogicalClusterAdmin) ||
		slices.Contains(groups, bootstrap.SystemKcpWorkspaceBootstrapper)
	// note: workspaceadmins are purposefully not considered to be privileged users for this plugin
	// as this would lead to cross-workspace privileged escalations.
}
