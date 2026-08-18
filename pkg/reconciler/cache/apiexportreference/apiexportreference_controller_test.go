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

package apiexportreference

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	cachev1alpha1apply "github.com/kcp-dev/sdk/client/applyconfiguration/cache/v1alpha1"
)

const testCluster = "root:org:provider"

func export(name string, refs ...corev1.TypedLocalObjectReference) *apisv1alpha2.APIExport {
	e := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			UID:         "export-uid",
			Annotations: map[string]string{logicalcluster.AnnotationKey: testCluster},
		},
	}
	for _, ref := range refs {
		e.Spec.Resources = append(e.Spec.Resources, apisv1alpha2.ResourceSchema{
			Group:  ptr.Deref(ref.APIGroup, ""),
			Name:   "cowboys",
			Schema: "today.cowboys.wildwest.dev",
			Storage: apisv1alpha2.ResourceSchemaStorage{
				Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{Reference: ref},
			},
		})
	}
	return e
}

func sheriffRef(name string) corev1.TypedLocalObjectReference {
	return corev1.TypedLocalObjectReference{
		APIGroup: ptr.To("wildwest.dev"),
		Kind:     "Sheriff",
		Name:     name,
	}
}

// ghostRef names a kind sheriffMapper does not know, which is what a CRD that
// has not been created -- or has not been Established yet -- looks like from
// here.
func ghostRef(name string) corev1.TypedLocalObjectReference {
	return corev1.TypedLocalObjectReference{
		APIGroup: ptr.To("nowhere.dev"),
		Kind:     "Ghost",
		Name:     name,
	}
}

func sheriffMapper(t *testing.T) func(cluster logicalcluster.Name, gk schema.GroupKind) (*meta.RESTMapping, error) {
	return func(cluster logicalcluster.Name, gk schema.GroupKind) (*meta.RESTMapping, error) {
		require.Equal(t, logicalcluster.Name(testCluster), cluster, "references must resolve in the APIExport's own cluster")
		if gk.Group == "wildwest.dev" && gk.Kind == "Sheriff" {
			return &meta.RESTMapping{
				Resource:         schema.GroupVersionResource{Group: "wildwest.dev", Version: "v1alpha1", Resource: "sheriffs"},
				GroupVersionKind: schema.GroupVersionKind{Group: "wildwest.dev", Version: "v1alpha1", Kind: "Sheriff"},
				Scope:            meta.RESTScopeRoot,
			}, nil
		}
		return nil, &meta.NoKindMatchError{GroupKind: gk}
	}
}

func TestReferencesFrom(t *testing.T) {
	t.Parallel()

	e := export("wildwest-provider", sheriffRef("one"))
	e.Spec.Resources = append(e.Spec.Resources,
		// Regular storage: not a reference.
		apisv1alpha2.ResourceSchema{Group: "wildwest.dev", Name: "cowboys", Schema: "today.cowboys.wildwest.dev"},
		// Virtual storage without kind or name: not a usable reference.
		apisv1alpha2.ResourceSchema{
			Group:   "wildwest.dev",
			Name:    "cowboys",
			Schema:  "today.cowboys.wildwest.dev",
			Storage: apisv1alpha2.ResourceSchemaStorage{Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{}},
		},
	)

	require.Equal(t,
		[]reference{{group: "wildwest.dev", kind: "Sheriff", name: "one"}},
		referencesFrom(e))
}

func TestNameFor(t *testing.T) {
	t.Parallel()

	ref := resolved{
		gvr:  schema.GroupVersionResource{Group: "wildwest.dev", Version: "v1alpha1", Resource: "sheriffs"},
		name: "one",
	}

	name := nameFor("wildwest-provider", ref)
	require.Equal(t, name, nameFor("wildwest-provider", ref), "the name has to be stable")
	require.Regexp(t, `^apiexport-sheriffs-[a-z0-9]+$`, name)

	require.NotEqual(t, name, nameFor("other-export", ref), "a different export must yield a different name")
	require.NotEqual(t, name, nameFor("wildwest-provider", resolved{gvr: ref.gvr, name: "two"}),
		"a different object must yield a different name")

	long := resolved{
		gvr:  schema.GroupVersionResource{Group: "example.org", Version: "v1", Resource: "someveryveryverylongresourcenamethatjustkeepsongoing"},
		name: "one",
	}
	require.LessOrEqual(t, len(nameFor("export", long)), 63, "the name has to stay a legal object name")
}

func TestOwnedBy(t *testing.T) {
	t.Parallel()

	ccr := &cachev1alpha1.ClusterCachedResource{
		ObjectMeta: metav1.ObjectMeta{
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: apisv1alpha2.SchemeGroupVersion.String(),
				Kind:       "APIExport",
				Name:       "wildwest-provider",
			}},
		},
	}
	require.True(t, ownedBy(ccr, "wildwest-provider"))
	require.False(t, ownedBy(ccr, "other-export"))
	require.False(t, ownedBy(&cachev1alpha1.ClusterCachedResource{}, "wildwest-provider"))

	wrongKind := ccr.DeepCopy()
	wrongKind.OwnerReferences[0].Kind = "Workspace"
	require.False(t, ownedBy(wrongKind, "wildwest-provider"))
}

func TestDesired(t *testing.T) {
	t.Parallel()

	e := export("wildwest-provider")
	ref := resolved{
		gvr:  schema.GroupVersionResource{Group: "wildwest.dev", Version: "v1alpha1", Resource: "sheriffs"},
		name: "one",
	}

	got := desired(e, ref)

	require.Equal(t, nameFor("wildwest-provider", ref), *got.Name)
	require.Len(t, got.OwnerReferences, 1)
	require.Equal(t, "APIExport", *got.OwnerReferences[0].Kind)
	require.Equal(t, "wildwest-provider", *got.OwnerReferences[0].Name)
	require.Equal(t, e.UID, *got.OwnerReferences[0].UID)
	require.Equal(t, "wildwest.dev", *got.Spec.Group)
	require.Equal(t, "v1alpha1", *got.Spec.Version)
	require.Equal(t, "sheriffs", *got.Spec.Resource)
	require.Equal(t, []string{"one"}, got.Spec.Names)
	require.Nil(t, got.Spec.Identity, "identity is not this controller's to manage")
}

func TestWantedFor(t *testing.T) {
	t.Parallel()

	t.Run("a deleting export wants nothing", func(t *testing.T) {
		t.Parallel()

		c := &controller{restMappingFor: sheriffMapper(t)}
		e := export("wildwest-provider", sheriffRef("one"))
		e.DeletionTimestamp = ptr.To(metav1.Now())

		wanted, unresolved, err := c.wantedFor(e)
		require.NoError(t, err)
		require.Empty(t, wanted)
		require.Empty(t, unresolved)
	})

	t.Run("no references, nothing wanted", func(t *testing.T) {
		t.Parallel()

		c := &controller{restMappingFor: sheriffMapper(t)}

		wanted, unresolved, err := c.wantedFor(export("wildwest-provider"))
		require.NoError(t, err)
		require.Empty(t, wanted)
		require.Empty(t, unresolved)
	})

	t.Run("a reference resolves to one ClusterCachedResource", func(t *testing.T) {
		t.Parallel()

		c := &controller{restMappingFor: sheriffMapper(t)}

		wanted, unresolved, err := c.wantedFor(export("wildwest-provider", sheriffRef("one")))
		require.NoError(t, err)
		require.Empty(t, unresolved)
		require.Len(t, wanted, 1)
		want := wanted[nameFor("wildwest-provider", resolved{
			gvr:  schema.GroupVersionResource{Group: "wildwest.dev", Version: "v1alpha1", Resource: "sheriffs"},
			name: "one",
		})]
		require.NotNil(t, want)
		require.Equal(t, []string{"one"}, want.Spec.Names)
	})

	t.Run("duplicate references collapse", func(t *testing.T) {
		t.Parallel()

		c := &controller{restMappingFor: sheriffMapper(t)}

		wanted, unresolved, err := c.wantedFor(export("wildwest-provider", sheriffRef("one"), sheriffRef("one")))
		require.NoError(t, err)
		require.Empty(t, unresolved)
		require.Len(t, wanted, 1)
	})

	t.Run("a kind that is not served is reported, not dropped", func(t *testing.T) {
		t.Parallel()

		c := &controller{restMappingFor: sheriffMapper(t)}

		wanted, unresolved, err := c.wantedFor(export("wildwest-provider", ghostRef("one")))
		require.NoError(t, err)
		require.Empty(t, wanted)
		require.Equal(t, []reference{{group: "nowhere.dev", kind: "Ghost", name: "one"}}, unresolved,
			"a no-match is 'not yet', and the caller has to hear about it to retry")
	})

	t.Run("a reference that resolves is kept while another does not", func(t *testing.T) {
		t.Parallel()

		c := &controller{restMappingFor: sheriffMapper(t)}

		wanted, unresolved, err := c.wantedFor(export("wildwest-provider", sheriffRef("one"), ghostRef("two")))
		require.NoError(t, err)
		require.Len(t, wanted, 1, "a settled reference is not held hostage by an unsettled one")
		require.Len(t, unresolved, 1)
	})

	t.Run("other mapper errors propagate", func(t *testing.T) {
		t.Parallel()

		boom := errors.New("boom")
		c := &controller{restMappingFor: func(logicalcluster.Name, schema.GroupKind) (*meta.RESTMapping, error) {
			return nil, boom
		}}

		_, _, err := c.wantedFor(export("wildwest-provider", sheriffRef("one")))
		require.ErrorIs(t, err, boom)
	})
}

func TestReconcile(t *testing.T) {
	t.Parallel()

	key := kcpcache.ToClusterAwareKey(testCluster, "", "wildwest-provider")

	owned := func(name string) *cachev1alpha1.ClusterCachedResource {
		return &cachev1alpha1.ClusterCachedResource{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Annotations: map[string]string{logicalcluster.AnnotationKey: testCluster},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: apisv1alpha2.SchemeGroupVersion.String(),
					Kind:       "APIExport",
					Name:       "wildwest-provider",
				}},
			},
		}
	}

	newController := func(e *apisv1alpha2.APIExport, existing []*cachev1alpha1.ClusterCachedResource, applied *[]string, deleted *[]string) *controller {
		return &controller{
			restMappingFor: sheriffMapper(t),
			getAPIExport: func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIExport, error) {
				if e == nil {
					return nil, apierrors.NewNotFound(schema.GroupResource{Group: "apis.kcp.io", Resource: "apiexports"}, name)
				}
				return e, nil
			},
			listOwnedResources: func(cluster logicalcluster.Name, export string) ([]*cachev1alpha1.ClusterCachedResource, error) {
				return existing, nil
			},
			applyResource: func(_ context.Context, _ logicalcluster.Name, ccr *cachev1alpha1apply.ClusterCachedResourceApplyConfiguration) error {
				*applied = append(*applied, *ccr.Name)
				return nil
			},
			deleteResource: func(_ context.Context, _ logicalcluster.Name, name string) error {
				*deleted = append(*deleted, name)
				return nil
			},
		}
	}

	wantedName := nameFor("wildwest-provider", resolved{
		gvr:  schema.GroupVersionResource{Group: "wildwest.dev", Version: "v1alpha1", Resource: "sheriffs"},
		name: "one",
	})

	t.Run("a gone export leaves everything to the garbage collector", func(t *testing.T) {
		t.Parallel()

		var applied, deleted []string
		c := newController(nil, []*cachev1alpha1.ClusterCachedResource{owned("stale")}, &applied, &deleted)

		require.NoError(t, c.reconcile(context.Background(), key))
		require.Empty(t, applied)
		require.Empty(t, deleted)
	})

	t.Run("what is referenced is applied, what is not goes", func(t *testing.T) {
		t.Parallel()

		var applied, deleted []string
		c := newController(
			export("wildwest-provider", sheriffRef("one")),
			[]*cachev1alpha1.ClusterCachedResource{owned("stale")},
			&applied, &deleted)

		require.NoError(t, c.reconcile(context.Background(), key))
		require.Equal(t, []string{wantedName}, applied)
		require.Equal(t, []string{"stale"}, deleted)
	})

	t.Run("a wanted resource that already exists is still applied, not skipped", func(t *testing.T) {
		t.Parallel()

		var applied, deleted []string
		c := newController(
			export("wildwest-provider", sheriffRef("one")),
			[]*cachev1alpha1.ClusterCachedResource{owned(wantedName)},
			&applied, &deleted)

		require.NoError(t, c.reconcile(context.Background(), key))
		require.Equal(t, []string{wantedName}, applied, "apply is how drift on owned fields is healed")
		require.Empty(t, deleted)
	})

	t.Run("a resource already on its way out is not deleted again", func(t *testing.T) {
		t.Parallel()

		var applied, deleted []string
		stale := owned("stale")
		stale.DeletionTimestamp = ptr.To(metav1.Now())
		c := newController(
			export("wildwest-provider", sheriffRef("one")),
			[]*cachev1alpha1.ClusterCachedResource{stale},
			&applied, &deleted)

		require.NoError(t, c.reconcile(context.Background(), key))
		require.Empty(t, deleted)
	})

	// The bootstrap race: a CRD and the APIExport naming its kind applied in the
	// same breath. The RESTMapper has not caught up, and if reconcile calls that
	// a finished job the APIExport is wedged until something else touches it --
	// no ClusterCachedResource, no replication, and discovery of the virtual
	// resource fails on every shard from then on.
	t.Run("an unresolved reference requeues instead of settling for nothing", func(t *testing.T) {
		t.Parallel()

		var applied, deleted []string
		c := newController(
			export("wildwest-provider", ghostRef("one")),
			nil, &applied, &deleted)

		err := c.reconcile(context.Background(), key)
		require.Error(t, err, "returning nil here is what makes the race permanent")
		require.Contains(t, err.Error(), "ghost.nowhere.dev/one")
		require.Empty(t, applied)
	})

	t.Run("nothing is pruned while a reference is unresolved", func(t *testing.T) {
		t.Parallel()

		var applied, deleted []string
		c := newController(
			export("wildwest-provider", ghostRef("one")),
			[]*cachev1alpha1.ClusterCachedResource{owned("not-actually-stale")},
			&applied, &deleted)

		require.Error(t, c.reconcile(context.Background(), key))
		require.Empty(t, deleted,
			"a partial wanted set is not evidence that a ClusterCachedResource is unreferenced")
	})

	t.Run("references that did resolve are applied even while others have not", func(t *testing.T) {
		t.Parallel()

		var applied, deleted []string
		c := newController(
			export("wildwest-provider", sheriffRef("one"), ghostRef("two")),
			nil, &applied, &deleted)

		require.Error(t, c.reconcile(context.Background(), key))
		require.Equal(t, []string{wantedName}, applied)
	})
}
