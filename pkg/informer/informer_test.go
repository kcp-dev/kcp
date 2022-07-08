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

package informer

import (
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kubernetes/pkg/api/genericcontrolplanescheme"
	_ "k8s.io/kubernetes/pkg/genericcontrolplane/apis/install"
)

// TestBuiltInInformableTypes tests that there is no drift between actual built-in types and the list that is hard-coded
// in builtInInformableTypes.
func TestBuiltInInformableTypes(t *testing.T) {
	builtInGVRs := map[schema.GroupVersionResource]struct{}{}

	// In the scheme, but not actual resources
	kindsToIgnore := sets.NewString(
		"List",
		"CreateOptions",
		"DeleteOptions",
		"GetOptions",
		"ListOptions",
		"PatchOptions",
		"UpdateOptions",
		"WatchEvent",
	)

	// Internal types and/or things that are not list/watchable
	gvksToIgnore := map[schema.GroupVersionKind]struct{}{
		{Version: "v1", Kind: "APIGroup"}:                                                {},
		{Version: "v1", Kind: "APIVersions"}:                                             {},
		{Version: "v1", Kind: "RangeAllocation"}:                                         {},
		{Version: "v1", Kind: "SerializedReference"}:                                     {},
		{Version: "v1", Kind: "Status"}:                                                  {},
		{Group: "authentication.k8s.io", Version: "v1", Kind: "TokenRequest"}:            {},
		{Group: "authentication.k8s.io", Version: "v1", Kind: "TokenReview"}:             {},
		{Group: "authorization.k8s.io", Version: "v1", Kind: "LocalSubjectAccessReview"}: {},
		{Group: "authorization.k8s.io", Version: "v1", Kind: "SelfSubjectAccessReview"}:  {},
		{Group: "authorization.k8s.io", Version: "v1", Kind: "SelfSubjectRulesReview"}:   {},
		{Group: "authorization.k8s.io", Version: "v1", Kind: "SubjectAccessReview"}:      {},
	}

	gvsToIgnore := map[schema.GroupVersion]struct{}{
		// Covered by Group=""
		{Group: "core", Version: "v1"}: {},

		// These are alpha/beta versions that are not preferred (they all have v1)
		{Group: "admissionregistration.k8s.io", Version: "v1beta1"}:  {},
		{Group: "authentication.k8s.io", Version: "v1beta1"}:         {},
		{Group: "authorization.k8s.io", Version: "v1beta1"}:          {},
		{Group: "certificates.k8s.io", Version: "v1beta1"}:           {},
		{Group: "coordination.k8s.io", Version: "v1beta1"}:           {},
		{Group: "events.k8s.io", Version: "v1beta1"}:                 {},
		{Group: "flowcontrol.apiserver.k8s.io", Version: "v1alpha1"}: {},
		{Group: "flowcontrol.apiserver.k8s.io", Version: "v1beta1"}:  {},
		{Group: "rbac.authorization.k8s.io", Version: "v1alpha1"}:    {},
		{Group: "rbac.authorization.k8s.io", Version: "v1beta1"}:     {},
	}

	allKnownTypes := genericcontrolplanescheme.Scheme.AllKnownTypes()

	// CRDs are not included in the genericcontrolplane scheme (because they're part of the apiextensions apiserver),
	// so we have to manually add them
	allKnownTypes[schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}] = reflect.TypeOf(struct{}{})

	for gvk := range allKnownTypes {
		if kindsToIgnore.Has(gvk.Kind) {
			continue
		}

		if _, found := gvsToIgnore[gvk.GroupVersion()]; found {
			continue
		}

		if _, found := gvksToIgnore[gvk]; found {
			continue
		}

		if strings.HasSuffix(gvk.Kind, "List") {
			continue
		}
		if gvk.Version == "__internal" {
			continue
		}

		resourceName := strings.ToLower(gvk.Kind) + "s"
		gvr := gvk.GroupVersion().WithResource(resourceName)

		builtInGVRs[gvr] = struct{}{}
	}

	require.Empty(t, cmp.Diff(builtInGVRs, builtInInformableTypes()))
}
