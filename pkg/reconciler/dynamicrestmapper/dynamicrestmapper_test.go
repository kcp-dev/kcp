/*
Copyright 2025 The kcp Authors.

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

package dynamicrestmapper

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/workqueue"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
)

func newDefaultRESTMapperWith(gvkrs []typeMeta) *DefaultRESTMapper {
	defaultGroupVersions := make(map[string]string)
	for _, typemeta := range gvkrs {
		if typemeta.Version > defaultGroupVersions[typemeta.Group] {
			defaultGroupVersions[typemeta.Group] = typemeta.Version
		}
	}

	mapper := NewDefaultRESTMapper(nil)
	for _, typemeta := range gvkrs {
		mapper.AddSpecific(
			typemeta.groupVersionKind(),
			typemeta.groupVersionResourcePlural(),
			typemeta.groupVersionResourceSingular(),
			meta.RESTScopeRoot,
		)
	}

	for g, v := range defaultGroupVersions {
		mapper.defaultGroupVersions = append(mapper.defaultGroupVersions, schema.GroupVersion{
			Group:   g,
			Version: v,
		})
	}

	return mapper
}

func TestClusterRESTMapping(t *testing.T) {
	t.Parallel()
	type applyPair struct {
		toRemove []typeMeta
		toAdd    []typeMeta
	}

	scenarios := map[string]struct {
		dmapper                   *DynamicRESTMapper
		applyPairs                map[logicalcluster.Name]applyPair
		expectedMappingsByCluster map[logicalcluster.Name]*DefaultRESTMapper
	}{
		// Empty dmapper should resolve to empty.
		"Empty dmapper should resolve to empty": {
			dmapper:                   NewDynamicRESTMapper(),
			expectedMappingsByCluster: map[logicalcluster.Name]*DefaultRESTMapper{},
		},
		// Single mapping should resolve to that mapping.
		"Single mapping should resolve to that mapping": {
			dmapper: NewDynamicRESTMapper(),
			applyPairs: map[logicalcluster.Name]applyPair{
				"one": {
					toAdd: []typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "object", "objects", meta.RESTScopeRoot),
					},
				},
			},
			expectedMappingsByCluster: map[logicalcluster.Name]*DefaultRESTMapper{
				"one": newDefaultRESTMapperWith([]typeMeta{
					newTypeMeta("api.example.com", "v1", "Object", "object", "objects", meta.RESTScopeRoot),
				}),
			},
		},
		// Removing from empty dmapper should resolve to empty.
		"Removing from empty dmapper should resolve to empty": {
			dmapper: NewDynamicRESTMapper(),
			applyPairs: map[logicalcluster.Name]applyPair{
				"one": {
					toRemove: []typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "object", "objects", meta.RESTScopeRoot),
					},
				},
			},
			expectedMappingsByCluster: map[logicalcluster.Name]*DefaultRESTMapper{},
		},
		// Removing and adding the same entry should resolve to adding that entry.
		// This case can be triggered by an unrelated change on the watched resource.
		"Removing and adding the same entry should resolve to adding that entry": {
			dmapper: NewDynamicRESTMapper(),
			applyPairs: map[logicalcluster.Name]applyPair{
				"one": {
					toRemove: []typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "object", "objects", meta.RESTScopeRoot),
					},
					toAdd: []typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "object", "objects", meta.RESTScopeRoot),
					},
				},
			},
			expectedMappingsByCluster: map[logicalcluster.Name]*DefaultRESTMapper{
				"one": newDefaultRESTMapperWith([]typeMeta{
					newTypeMeta("api.example.com", "v1", "Object", "object", "objects", meta.RESTScopeRoot),
				}),
			},
		},
		// Removing an entry and adding the same entry and an another one should resolve into having two entries.
		// This could be triggered by e.g. adding a new resource version to a CRD.
		"Removing an entry and adding the same entry and an another one should resolve into having two entries": {
			dmapper: NewDynamicRESTMapper(),
			applyPairs: map[logicalcluster.Name]applyPair{
				"one": {
					toRemove: []typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "object", "objects", meta.RESTScopeRoot),
					},
					toAdd: []typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "object", "objects", meta.RESTScopeRoot),
						newTypeMeta("api.example.com", "v2", "Object", "object", "objects", meta.RESTScopeRoot),
					},
				},
			},
			expectedMappingsByCluster: map[logicalcluster.Name]*DefaultRESTMapper{
				"one": newDefaultRESTMapperWith([]typeMeta{
					newTypeMeta("api.example.com", "v1", "Object", "object", "objects", meta.RESTScopeRoot),
					newTypeMeta("api.example.com", "v2", "Object", "object", "objects", meta.RESTScopeRoot),
				}),
			},
		},
		// Removing an existing entry and adding a new one should resolve into having only the new entry.
		// This could be triggered by e.g. deprecating an older version of a resource and adding a new one.
		"Removing an existing entry and adding a new one should resolve into having only the new entry": {
			dmapper: &DynamicRESTMapper{
				dynamic: map[logicalcluster.Name]*DefaultRESTMapper{
					"one": newDefaultRESTMapperWith([]typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "object", "objects", meta.RESTScopeRoot),
					}),
				},
			},
			applyPairs: map[logicalcluster.Name]applyPair{
				"one": {
					toRemove: []typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "object", "objects", meta.RESTScopeRoot),
					},
					toAdd: []typeMeta{
						newTypeMeta("api.example.com", "v2", "Object", "object", "objects", meta.RESTScopeRoot),
					},
				},
			},
			expectedMappingsByCluster: map[logicalcluster.Name]*DefaultRESTMapper{
				"one": newDefaultRESTMapperWith([]typeMeta{
					newTypeMeta("api.example.com", "v2", "Object", "object", "objects", meta.RESTScopeRoot),
				}),
			},
		},
		// Removing all existing resources for a cluster should resolve to empty.
		"Removing all existing resources for a cluster should resolve to empty": {
			dmapper: &DynamicRESTMapper{
				dynamic: map[logicalcluster.Name]*DefaultRESTMapper{
					"one": newDefaultRESTMapperWith([]typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "object", "objects", meta.RESTScopeRoot),
						newTypeMeta("api.example.com", "v2", "Object", "object", "objects", meta.RESTScopeRoot),
					}),
				},
			},
			applyPairs: map[logicalcluster.Name]applyPair{
				"one": {
					toRemove: []typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "object", "objects", meta.RESTScopeRoot),
						newTypeMeta("api.example.com", "v2", "Object", "object", "objects", meta.RESTScopeRoot),
					},
				},
			},
			expectedMappingsByCluster: map[logicalcluster.Name]*DefaultRESTMapper{},
		},
		// Check that changes with more clusters are mapped correctly.
		"Check that changes with more clusters are mapped correctly": {
			dmapper: &DynamicRESTMapper{
				dynamic: map[logicalcluster.Name]*DefaultRESTMapper{
					"one": newDefaultRESTMapperWith([]typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "object", "objects", meta.RESTScopeRoot),
					}),
					"two": newDefaultRESTMapperWith([]typeMeta{
						newTypeMeta("api.example.com", "v2", "Object", "object", "objects", meta.RESTScopeRoot),
					}),
				},
			},
			applyPairs: map[logicalcluster.Name]applyPair{
				"one": {
					toRemove: []typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "object", "objects", meta.RESTScopeRoot),
					},
					toAdd: []typeMeta{
						newTypeMeta("api.example.com", "v2", "Object", "object", "objects", meta.RESTScopeRoot),
					},
				},
				"two": {
					toRemove: []typeMeta{
						newTypeMeta("api.example.com", "v2", "Object", "object", "objects", meta.RESTScopeRoot),
					},
					toAdd: []typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "object", "objects", meta.RESTScopeRoot),
					},
				},
			},
			expectedMappingsByCluster: map[logicalcluster.Name]*DefaultRESTMapper{
				"one": newDefaultRESTMapperWith([]typeMeta{
					newTypeMeta("api.example.com", "v2", "Object", "object", "objects", meta.RESTScopeRoot),
				}),
				"two": newDefaultRESTMapperWith([]typeMeta{
					newTypeMeta("api.example.com", "v1", "Object", "object", "objects", meta.RESTScopeRoot),
				}),
			},
		},
	}

	for testName, s := range scenarios {
		t.Run(testName, func(t *testing.T) {
			t.Parallel()
			for clusterName, apply := range s.applyPairs {
				s.dmapper.ForCluster(clusterName).apply(apply.toRemove, apply.toAdd)
			}

			require.Equal(t, s.expectedMappingsByCluster, s.dmapper.dynamic,
				"DynamicRESTMapper contains unexpected mapping")
		})
	}

	// Test use-before-create.

	objTypeMeta := newTypeMeta("api.example.com", "v1", "Object", "", "", meta.RESTScopeRoot)
	dmapper := NewDynamicRESTMapper()
	oneMapper := dmapper.ForCluster("one")
	require.NotNil(t, oneMapper, "DynamicRESTMapper.ForCluster() should never return nil")

	res, err := oneMapper.ResourceFor(objTypeMeta.groupVersionResourcePlural())
	require.Equal(t, schema.GroupVersionResource{}, res,
		"ResourceFor() on an empty mapper should return empty result")
	require.ErrorIs(t, err, &meta.NoResourceMatchError{},
		"ResourceFor() on an empty mapper should return an error of type NoResourceMatchError")

	// Test use-after-create.

	dmapper.ForCluster("one").apply(nil, []typeMeta{objTypeMeta})
	res, err = oneMapper.ResourceFor(objTypeMeta.groupVersionResourceSingular())
	require.NoError(t, err,
		"ResourceFor() on match should not return an error")
	require.Equal(t, objTypeMeta.groupVersionResourcePlural(), res,
		"ResourceFor() on match should return non-empty result")

	// Test use-after-delete.

	dmapper.ForCluster("one").apply([]typeMeta{objTypeMeta}, nil)
	res, err = oneMapper.ResourceFor(objTypeMeta.groupVersionResourceSingular())
	require.Equal(t, schema.GroupVersionResource{}, res,
		"ResourceFor() on an empty mapper should return empty result")
	require.ErrorIs(t, err, &meta.NoResourceMatchError{},
		"ResourceFor() on an empty mapper should return an error of type NoResourceMatchError")
}

func TestRESTMappingVersionFallback(t *testing.T) {
	t.Parallel()

	// apis.kcp.io defaults to v1alpha2 because of APIExport, while
	// APIExportEndpointSlice only exists in v1alpha1. Mapping the latter
	// without an explicit version must fall back to v1alpha1 instead of
	// failing.
	exportV1alpha1 := newTypeMeta("apis.kcp.io", "v1alpha1", "APIExport", "apiexport", "apiexports", meta.RESTScopeRoot)
	exportV1alpha2 := newTypeMeta("apis.kcp.io", "v1alpha2", "APIExport", "apiexport", "apiexports", meta.RESTScopeRoot)
	sliceV1alpha1 := newTypeMeta("apis.kcp.io", "v1alpha1", "APIExportEndpointSlice", "apiexportendpointslice", "apiexportendpointslices", meta.RESTScopeRoot)

	dmapper := NewDynamicRESTMapper()
	dmapper.ForCluster("one").apply(nil, []typeMeta{exportV1alpha1, exportV1alpha2, sliceV1alpha1})
	mapper := dmapper.ForCluster("one")

	mapping, err := mapper.RESTMapping(sliceV1alpha1.groupVersionKind().GroupKind())
	require.NoError(t, err,
		"RESTMapping() should fall back to a version serving the kind")
	require.Equal(t, sliceV1alpha1.groupVersionKind(), mapping.GroupVersionKind,
		"RESTMapping() fallback should return the newest version serving the kind")

	mappings, err := mapper.RESTMappings(sliceV1alpha1.groupVersionKind().GroupKind())
	require.NoError(t, err,
		"RESTMappings() should fall back to versions serving the kind")
	require.Len(t, mappings, 1)
	require.Equal(t, sliceV1alpha1.groupVersionKind(), mappings[0].GroupVersionKind)

	// The default version must still win when it serves the kind.
	mapping, err = mapper.RESTMapping(exportV1alpha2.groupVersionKind().GroupKind())
	require.NoError(t, err)
	require.Equal(t, exportV1alpha2.groupVersionKind(), mapping.GroupVersionKind,
		"RESTMapping() should keep preferring the group's default version")

	// An explicit version request must not trigger the fallback.
	_, err = mapper.RESTMapping(sliceV1alpha1.groupVersionKind().GroupKind(), "v1alpha2")
	require.Error(t, err,
		"RESTMapping() with an explicit unserved version should still fail")

	// An unknown kind must still fail.
	_, err = mapper.RESTMapping(schema.GroupKind{Group: "apis.kcp.io", Kind: "Unknown"})
	require.Error(t, err,
		"RESTMapping() for an unknown kind should fail")
}

func TestDiffResourceBindingsAnn(t *testing.T) {
	t.Parallel()
	scenarios := map[string]struct {
		oldAnn apibinding.ResourceBindingsAnnotation
		newAnn apibinding.ResourceBindingsAnnotation

		expectedToAdd    apibinding.ResourceBindingsAnnotation
		expectedToRemove apibinding.ResourceBindingsAnnotation
	}{
		// Only old.
		"Only old": {
			oldAnn: apibinding.ResourceBindingsAnnotation{
				"1": {},
			},
			expectedToRemove: apibinding.ResourceBindingsAnnotation{
				"1": {},
			},
			expectedToAdd: make(apibinding.ResourceBindingsAnnotation),
		},
		// Only new.
		"Only new": {
			newAnn: apibinding.ResourceBindingsAnnotation{
				"1": {},
			},
			expectedToRemove: make(apibinding.ResourceBindingsAnnotation),
			expectedToAdd: apibinding.ResourceBindingsAnnotation{
				"1": {},
			},
		},
		// Identical new and old annotations should cause no changes.
		"Identical new and old annotations should cause no changes": {
			oldAnn: apibinding.ResourceBindingsAnnotation{
				"1": {},
			},
			newAnn: apibinding.ResourceBindingsAnnotation{
				"1": {},
			},
			expectedToAdd:    make(apibinding.ResourceBindingsAnnotation),
			expectedToRemove: make(apibinding.ResourceBindingsAnnotation),
		},
		// New annotation adds an entry to the old one.
		"New annotation adds an entry to the old one": {
			oldAnn: apibinding.ResourceBindingsAnnotation{
				"1": {},
			},
			newAnn: apibinding.ResourceBindingsAnnotation{
				"1": {},
				"2": {},
			},
			expectedToRemove: make(apibinding.ResourceBindingsAnnotation),
			expectedToAdd: apibinding.ResourceBindingsAnnotation{
				"2": {},
			},
		},
		// New annotation removes an entry that was in the old one.
		"New annotation removes an entry that was in the old one": {
			oldAnn: apibinding.ResourceBindingsAnnotation{
				"1": {},
				"2": {},
			},
			newAnn: apibinding.ResourceBindingsAnnotation{
				"1": {},
			},
			expectedToRemove: apibinding.ResourceBindingsAnnotation{
				"2": {},
			},
			expectedToAdd: make(apibinding.ResourceBindingsAnnotation),
		},
		// New annotation removes an entry that was in the old annotation, but also adds a new one.
		"New annotation removes an entry that was in the old annotation, but also adds a new one": {
			oldAnn: apibinding.ResourceBindingsAnnotation{
				"1": {},
				"2": {},
			},
			newAnn: apibinding.ResourceBindingsAnnotation{
				"1": {},
				"3": {},
			},
			expectedToAdd: apibinding.ResourceBindingsAnnotation{
				"3": {},
			},
			expectedToRemove: apibinding.ResourceBindingsAnnotation{
				"2": {},
			},
		},
	}

	for testName, s := range scenarios {
		t.Run(testName, func(t *testing.T) {
			t.Parallel()
			toRemove, toAdd :=
				diffResourceBindingsAnn(s.oldAnn, s.newAnn)
			require.Equal(t, s.expectedToRemove, toRemove,
				"mismatch in annotation keys to remove")
			require.Equal(t, s.expectedToAdd, toAdd,
				"mismatch in annotation keys to add")
		})
	}
}

// TestGatherGVKRsForCRD guards against a race where the resource-bindings
// lock for a CRD becomes visible before the CRD itself is established:
// reading Status.AcceptedNames at that point would yield empty names and
// store a garbage mapping that is never corrected.
func TestGatherGVKRsForCRD(t *testing.T) {
	t.Parallel()

	newCRD := func(established bool) *apiextensionsv1.CustomResourceDefinition {
		crd := &apiextensionsv1.CustomResourceDefinition{
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: "example.com",
				Scope: apiextensionsv1.ClusterScoped,
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{Name: "v1", Served: true},
				},
			},
		}
		if established {
			// AcceptedNames is only populated once the CRD has been
			// reconciled to Established; that's the actual race being
			// guarded against, not just the condition being absent.
			crd.Status.AcceptedNames = apiextensionsv1.CustomResourceDefinitionNames{
				Kind:     "Widget",
				Singular: "widget",
				Plural:   "widgets",
			}
			crd.Status.Conditions = append(crd.Status.Conditions, apiextensionsv1.CustomResourceDefinitionCondition{
				Type:   apiextensionsv1.Established,
				Status: apiextensionsv1.ConditionTrue,
			})
		}
		return crd
	}

	c := &DynamicTypesController{}

	t.Run("not established yields no mappings and signals a retry", func(t *testing.T) {
		t.Parallel()
		gvkrs, notEstablished := c.gatherGVKRsForCRD(newCRD(false))
		require.Empty(t, gvkrs)
		require.True(t, notEstablished)
	})

	t.Run("established yields the accepted names", func(t *testing.T) {
		t.Parallel()
		gvkrs, notEstablished := c.gatherGVKRsForCRD(newCRD(true))
		require.False(t, notEstablished)
		require.Equal(t, []typeMeta{
			newTypeMeta("example.com", "v1", "Widget", "widget", "widgets", meta.RESTScopeRoot),
		}, gvkrs)
	})
}

// TestProcessRequeuesNotEstablishedCRD checks that a queue item covering
// several bound resources still applies the mappings it can gather even
// when one of them is a CRD that hasn't reached Established yet, and that
// the item is requeued with backoff (rather than the whole batch failing)
// so the not-yet-established CRD is retried later. It drives the change
// through processNextWorkItem (not just process) because the backoff only
// actually escalates if processNextWorkItem calls AddRateLimited instead of
// Forget when process reports requeue=true; calling process directly can't
// observe that distinction.
func TestProcessRequeuesNotEstablishedCRD(t *testing.T) {
	t.Parallel()

	clusterName := logicalcluster.Name("root:org:ws")

	establishedCRD := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "widgets.example.com"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "example.com",
			Scope: apiextensionsv1.ClusterScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1", Served: true},
			},
		},
		Status: apiextensionsv1.CustomResourceDefinitionStatus{
			AcceptedNames: apiextensionsv1.CustomResourceDefinitionNames{
				Kind:     "Widget",
				Singular: "widget",
				Plural:   "widgets",
			},
			Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
				{Type: apiextensionsv1.Established, Status: apiextensionsv1.ConditionTrue},
			},
		},
	}
	pendingCRD := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "gadgets.example.com"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "example.com",
			Scope: apiextensionsv1.ClusterScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1", Served: true},
			},
		},
	}

	c := &DynamicTypesController{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "test"},
		),
		state: NewDynamicRESTMapper(),
		getLogicalCluster: func(logicalcluster.Name, string) (*corev1alpha1.LogicalCluster, error) {
			return &corev1alpha1.LogicalCluster{}, nil
		},
		getCRD: func(_ logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
			switch name {
			case establishedCRD.Name:
				return establishedCRD, nil
			case pendingCRD.Name:
				return pendingCRD, nil
			default:
				return nil, apierrors.NewNotFound(apiextensionsv1.Resource("customresourcedefinitions"), name)
			}
		},
	}

	item := queueItem{
		ClusterName:         clusterName,
		ClusterResourceName: corev1alpha1.LogicalClusterName,
		Op:                  opUpdate,
		ToAdd: apibinding.ResourceBindingsAnnotation{
			establishedCRD.Name: {Lock: apibinding.Lock{CRD: true}},
			pendingCRD.Name:     {Lock: apibinding.Lock{CRD: true}},
		},
	}
	keyBytes, err := json.Marshal(&item)
	require.NoError(t, err)
	key := string(keyBytes)
	t.Cleanup(c.queue.ShutDown)

	c.queue.Add(key)
	require.True(t, c.processNextWorkItem(context.Background()))

	gvkrs, err := c.state.ForCluster(clusterName).getGVKRs(schema.GroupResource{Group: "example.com", Resource: "widgets"})
	require.NoError(t, err)
	require.NotEmpty(t, gvkrs, "the established CRD's mapping should still be applied")

	require.Equal(t, 1, c.queue.NumRequeues(key),
		"processNextWorkItem must call AddRateLimited (not Forget) when process reports requeue, "+
			"otherwise the per-item exponential backoff never escalates for a CRD that never establishes")
}
