/*
Copyright 2026 The kcp Authors.

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

package apireconciler

import (
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2/ktesting"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	apisv1alpha1listers "github.com/kcp-dev/sdk/client/listers/apis/v1alpha1"
)

const providerCluster = "provider"

func subresourceSchema(name, kind string, versions ...apisv1alpha1.APIResourceVersion) *apisv1alpha1.APIResourceSchema {
	return &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{logicalcluster.AnnotationKey: providerCluster},
		},
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group:    "wildwest.dev",
			Names:    apiextensionsv1.CustomResourceDefinitionNames{Kind: kind, Plural: name},
			Versions: versions,
		},
	}
}

func version(name string, served bool) apisv1alpha1.APIResourceVersion {
	return apisv1alpha1.APIResourceVersion{Name: name, Served: served}
}

func subresourceEntry(name, schemaName string) apisv1alpha2.ResourceSchema {
	return apisv1alpha2.ResourceSchema{
		Group:  "wildwest.dev",
		Name:   name,
		Schema: schemaName,
		Storage: apisv1alpha2.ResourceSchemaStorage{
			Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{},
		},
	}
}

func reconcilerWithSchemas(schemas ...*apisv1alpha1.APIResourceSchema) *APIReconciler {
	indexer := cache.NewIndexer(kcpcache.MetaClusterNamespaceKeyFunc, cache.Indexers{
		kcpcache.ClusterIndexName: kcpcache.ClusterIndexFunc,
	})
	for _, s := range schemas {
		if err := indexer.Add(s); err != nil {
			panic(err)
		}
	}
	return &APIReconciler{apiResourceSchemaLister: apisv1alpha1listers.NewAPIResourceSchemaClusterLister(indexer)}
}

// TestCustomSubresourcesFrom covers which subresource entries of an APIExport
// this virtual workspace serves, and the kind it serves each one as.
func TestCustomSubresourcesFrom(t *testing.T) {
	t.Parallel()

	cowboys := schema.GroupResource{Group: "wildwest.dev", Resource: "cowboys"}

	export := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "wildwest",
			Annotations: map[string]string{logicalcluster.AnnotationKey: providerCluster},
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{Group: "wildwest.dev", Name: "cowboys", Schema: "today.cowboys.wildwest.dev"},
				subresourceEntry("cowboys/shoot", "today.shoot.wildwest.dev"),
				subresourceEntry("cowboys/holster", "today.holster.wildwest.dev"),
				// Belongs to another resource of the same export.
				subresourceEntry("sheriffs/deputize", "today.deputize.wildwest.dev"),
			},
		},
	}

	c := reconcilerWithSchemas(
		subresourceSchema("today.shoot.wildwest.dev", "Shot", version("v1alpha1", true), version("v1beta1", true)),
		subresourceSchema("today.holster.wildwest.dev", "Holstering", version("v1beta1", true)),
		subresourceSchema("today.deputize.wildwest.dev", "Deputization", version("v1alpha1", true)),
	)

	names := func(subs []CustomSubresource) []string {
		out := make([]string, 0, len(subs))
		for _, sub := range subs {
			out = append(out, sub.Name)
		}
		return out
	}

	t.Run("own export serves every entry under the resource", func(t *testing.T) {
		t.Parallel()

		_, ctx := ktesting.NewTestContext(t)
		subs := c.customSubresourcesFrom(ctx, subresourceSource{export: export, own: true}, cowboys, "v1alpha1")
		require.ElementsMatch(t, []string{"shoot", "holster"}, names(subs))
		for _, sub := range subs {
			require.Equal(t, allSubresourceVerbs, sub.Verbs)
		}
	})

	t.Run("claimed export serves only what is claimed, with the claim's verbs", func(t *testing.T) {
		t.Parallel()

		_, ctx := ktesting.NewTestContext(t)
		subs := c.customSubresourcesFrom(ctx, subresourceSource{
			export:       export,
			claimedVerbs: map[string][]string{"shoot": {"create"}},
		}, cowboys, "v1alpha1")

		require.Len(t, subs, 1)
		require.Equal(t, "shoot", subs[0].Name)
		require.Equal(t, []string{"create"}, subs[0].Verbs)
	})

	t.Run("the subresource speaks its own kind, at the parent's version where it serves one", func(t *testing.T) {
		t.Parallel()

		_, ctx := ktesting.NewTestContext(t)
		subs := c.customSubresourcesFrom(ctx, subresourceSource{export: export, own: true}, cowboys, "v1alpha1")

		byName := map[string]CustomSubresource{}
		for _, sub := range subs {
			byName[sub.Name] = sub
		}

		require.Equal(t, schema.GroupVersionKind{Group: "wildwest.dev", Version: "v1alpha1", Kind: "Shot"}, byName["shoot"].Kind)
		// holster serves no v1alpha1, so it falls back to the version it does serve.
		require.Equal(t, schema.GroupVersionKind{Group: "wildwest.dev", Version: "v1beta1", Kind: "Holstering"}, byName["holster"].Kind)
	})

	t.Run("an entry whose schema is missing or unserved is skipped", func(t *testing.T) {
		t.Parallel()

		_, ctx := ktesting.NewTestContext(t)
		c := reconcilerWithSchemas(
			subresourceSchema("today.holster.wildwest.dev", "Holstering", version("v1beta1", false)),
		)
		subs := c.customSubresourcesFrom(ctx, subresourceSource{export: export, own: true}, cowboys, "v1alpha1")
		require.Empty(t, subs)
	})

	t.Run("a claim on the parent alone carries no subresource", func(t *testing.T) {
		t.Parallel()

		_, ctx := ktesting.NewTestContext(t)
		subs := c.customSubresourcesFrom(ctx, subresourceSource{export: export}, cowboys, "v1alpha1")
		require.Empty(t, subs)
	})
}

// TestSubresourcesFingerprint covers that a definition is rebuilt when its subresources
// change and reused when they do not, which is all the fingerprint is for.
func TestSubresourcesFingerprint(t *testing.T) {
	t.Parallel()

	gvk := schema.GroupVersionKind{Group: "wildwest.dev", Version: "v1alpha1", Kind: "Shot"}
	shoot := CustomSubresource{Name: "shoot", Kind: gvk, Verbs: []string{"create"}}
	holster := CustomSubresource{Name: "holster", Kind: gvk, Verbs: []string{"create"}}

	require.Equal(t, subresourcesFingerprint([]CustomSubresource{shoot, holster}), subresourcesFingerprint([]CustomSubresource{holster, shoot}),
		"order of the entries is not a difference")

	shootUpdatable := shoot
	shootUpdatable.Verbs = []string{"create", "update"}
	require.NotEqual(t, subresourcesFingerprint([]CustomSubresource{shoot}), subresourcesFingerprint([]CustomSubresource{shootUpdatable}),
		"a verb added to a claim changes which methods are served")

	shootV2 := shoot
	shootV2.Kind.Version = "v1beta1"
	require.NotEqual(t, subresourcesFingerprint([]CustomSubresource{shoot}), subresourcesFingerprint([]CustomSubresource{shootV2}),
		"a re-pointed schema changes the kind served")

	require.NotEqual(t, subresourcesFingerprint([]CustomSubresource{shoot}), subresourcesFingerprint(nil),
		"losing the only entry is a change")
}
