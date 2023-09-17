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

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/restmapper"

	kcpscheme "github.com/kcp-dev/kcp/pkg/server/scheme"
)

// TestBuiltInInformableTypes tests that there is no drift between actual built-in types and the list that is hard-coded
// in builtInInformableTypes.
func TestBuiltInInformableTypes(t *testing.T) {
	builtInGVRs := map[schema.GroupVersionResource]struct{}{}

	// In the scheme, but not actual resources
	kindsToIgnore := sets.New[string](
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
		{Group: "authentication.k8s.io", Version: "v1alpha1", Kind: "SelfSubjectReview"}: {},
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
		{Group: "admissionregistration.k8s.io", Version: "v1beta1"}: {},
		{Group: "authentication.k8s.io", Version: "v1beta1"}:        {},
		{Group: "authorization.k8s.io", Version: "v1beta1"}:         {},
		{Group: "certificates.k8s.io", Version: "v1beta1"}:          {},
		{Group: "coordination.k8s.io", Version: "v1beta1"}:          {},
		{Group: "events.k8s.io", Version: "v1beta1"}:                {},
		{Group: "rbac.authorization.k8s.io", Version: "v1alpha1"}:   {},
		{Group: "rbac.authorization.k8s.io", Version: "v1beta1"}:    {},
	}

	allKnownTypes := kcpscheme.Scheme.AllKnownTypes()

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

		// Handle e.g. ValidatingAdmissionPolicy -> validatingadmissionpolicies
		if strings.HasSuffix(gvk.Kind, "y") {
			resourceName = resourceName[:len(resourceName)-2] + "ies"
		}

		gvr := gvk.GroupVersion().WithResource(resourceName)

		builtInGVRs[gvr] = struct{}{}
	}

	builtInTypes := map[schema.GroupVersionResource]struct{}{}
	for gvr := range builtInInformableTypes {
		builtInTypes[gvr] = struct{}{}
	}

	require.Empty(t, cmp.Diff(builtInGVRs, builtInTypes))
}

func TestGVRsToDiscoveryData(t *testing.T) {
	input := map[schema.GroupVersionResource]GVRPartialMetadata{
		{Group: "g1", Version: "v1", Resource: "g1-v1-r1"}: {
			Scope: apiextensionsv1.ClusterScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind:     "G1v1r1",
				Singular: "g1v1r1",
			},
		},
		{Group: "g2", Version: "v1", Resource: "g2-v1-r1"}: {
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind:     "G2v1r1",
				Singular: "g2v1r1",
			},
		},
		{Group: "g1", Version: "v1", Resource: "g1-v1-r2"}: {
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind:     "G1v1r2",
				Singular: "g1v1r2",
			},
		},
		{Group: "g3", Version: "v3", Resource: "g3-v3-r1"}: {
			Scope: apiextensionsv1.ClusterScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind:     "G3v3r1",
				Singular: "g3v3r1",
			},
		},
	}

	expected := discoveryData{
		apiGroupResources: []*restmapper.APIGroupResources{
			{
				Group: metav1.APIGroup{
					Name: "g1",
					Versions: []metav1.GroupVersionForDiscovery{
						{
							GroupVersion: "g1",
							Version:      "v1",
						},
					},
				},
				VersionedResources: map[string][]metav1.APIResource{
					"v1": {
						{
							Name:         "g1-v1-r1",
							SingularName: "g1v1r1",
							Group:        "g1",
							Version:      "v1",
							Kind:         "G1v1r1",
							Verbs:        []string{"create", "list", "watch", "delete"},
						},
						{
							Name:         "g1-v1-r2",
							SingularName: "g1v1r2",
							Namespaced:   true,
							Group:        "g1",
							Version:      "v1",
							Kind:         "G1v1r2",
							Verbs:        []string{"create", "list", "watch", "delete"},
						},
					},
				},
			},
			{
				Group: metav1.APIGroup{
					Name: "g2",
					Versions: []metav1.GroupVersionForDiscovery{
						{
							GroupVersion: "g2",
							Version:      "v1",
						},
					},
				},
				VersionedResources: map[string][]metav1.APIResource{
					"v1": {
						{
							Name:         "g2-v1-r1",
							SingularName: "g2v1r1",
							Namespaced:   true,
							Group:        "g2",
							Version:      "v1",
							Kind:         "G2v1r1",
							Verbs:        []string{"create", "list", "watch", "delete"},
						},
					},
				},
			},
			{
				Group: metav1.APIGroup{
					Name: "g3",
					Versions: []metav1.GroupVersionForDiscovery{
						{
							GroupVersion: "g3",
							Version:      "v3",
						},
					},
				},
				VersionedResources: map[string][]metav1.APIResource{
					"v3": {
						{
							Name:         "g3-v3-r1",
							Group:        "g3",
							Version:      "v3",
							Namespaced:   false,
							Kind:         "G3v3r1",
							SingularName: "g3v3r1",
							Verbs:        []string{"create", "list", "watch", "delete"},
						},
					},
				},
			},
		},
		apiResourceList: []*metav1.APIResourceList{
			{
				GroupVersion: "g1/v1",
				APIResources: []metav1.APIResource{
					{
						Name:         "g1-v1-r1",
						Group:        "g1",
						Version:      "v1",
						Namespaced:   false,
						Kind:         "G1v1r1",
						SingularName: "g1v1r1",
						Verbs:        []string{"create", "list", "watch", "delete"},
					},
					{
						Name:         "g1-v1-r2",
						Group:        "g1",
						Version:      "v1",
						Namespaced:   true,
						Kind:         "G1v1r2",
						SingularName: "g1v1r2",
						Verbs:        []string{"create", "list", "watch", "delete"},
					},
				},
			},
			{
				GroupVersion: "g2/v1",
				APIResources: []metav1.APIResource{
					{
						Name:         "g2-v1-r1",
						Group:        "g2",
						Version:      "v1",
						Namespaced:   true,
						Kind:         "G2v1r1",
						SingularName: "g2v1r1",
						Verbs:        []string{"create", "list", "watch", "delete"},
					},
				},
			},
			{
				GroupVersion: "g3/v3",
				APIResources: []metav1.APIResource{
					{
						Name:         "g3-v3-r1",
						Group:        "g3",
						Version:      "v3",
						Namespaced:   false,
						Kind:         "G3v3r1",
						SingularName: "g3v3r1",
						Verbs:        []string{"create", "list", "watch", "delete"},
					},
				},
			},
		},
	}

	actual := gvrsToDiscoveryData(input)

	require.Empty(t, cmp.Diff(expected, actual, cmp.AllowUnexported(discoveryData{})))
}
