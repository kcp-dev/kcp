/*
Copyright 2025 The KCP Authors.

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
	"testing"

	"github.com/stretchr/testify/assert"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
)

func newDefaultRESTMapperWith(gvkrs []typeMeta) *DefaultRESTMapper {
	mapper := NewDefaultRESTMapper(nil)
	for _, typemeta := range gvkrs {
		mapper.AddSpecific(
			typemeta.groupVersionKind(),
			typemeta.groupVersionResourcePlural(),
			typemeta.groupVersionResourceSingular(),
			meta.RESTScopeRoot,
		)
	}
	return mapper
}

func TestClusterRESTMapping(t *testing.T) {
	type applyPair struct {
		toRemove []typeMeta
		toAdd    []typeMeta
	}

	scenarios := []struct {
		dmapper                   *DynamicRESTMapper
		applyPairs                map[logicalcluster.Name]applyPair
		expectedMappingsByCluster map[logicalcluster.Name]*DefaultRESTMapper
	}{
		// Empty dmapper should resolve to empty.
		{
			dmapper:                   NewDynamicRESTMapper(nil),
			expectedMappingsByCluster: map[logicalcluster.Name]*DefaultRESTMapper{},
		},
		// Single mapping should resolve to that mapping.
		{
			dmapper: NewDynamicRESTMapper(nil),
			applyPairs: map[logicalcluster.Name]applyPair{
				"one": {
					toAdd: []typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "", "", meta.RESTScopeRoot),
					},
				},
			},
			expectedMappingsByCluster: map[logicalcluster.Name]*DefaultRESTMapper{
				"one": newDefaultRESTMapperWith([]typeMeta{
					newTypeMeta("api.example.com", "v1", "Object", "", "", meta.RESTScopeRoot),
				}),
			},
		},
		// Removing from empty dmapper should resolve to empty.
		{
			dmapper: NewDynamicRESTMapper(nil),
			applyPairs: map[logicalcluster.Name]applyPair{
				"one": {
					toRemove: []typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "", "", meta.RESTScopeRoot),
					},
				},
			},
			expectedMappingsByCluster: map[logicalcluster.Name]*DefaultRESTMapper{},
		},
		// Removing and adding the same entry should resolve to adding that entry.
		// This case can be triggered by an unrelated change on the watched resource.
		{
			dmapper: NewDynamicRESTMapper(nil),
			applyPairs: map[logicalcluster.Name]applyPair{
				"one": {
					toRemove: []typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "", "", meta.RESTScopeRoot),
					},
					toAdd: []typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "", "", meta.RESTScopeRoot),
					},
				},
			},
			expectedMappingsByCluster: map[logicalcluster.Name]*DefaultRESTMapper{
				"one": newDefaultRESTMapperWith([]typeMeta{
					newTypeMeta("api.example.com", "v1", "Object", "", "", meta.RESTScopeRoot),
				}),
			},
		},
		// Removing an entry and adding the same entry and an another one should resolve into having two entries.
		// This could be triggered by e.g. adding a new resource version to a CRD.
		{
			dmapper: NewDynamicRESTMapper(nil),
			applyPairs: map[logicalcluster.Name]applyPair{
				"one": {
					toRemove: []typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "", "", meta.RESTScopeRoot),
					},
					toAdd: []typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "", "", meta.RESTScopeRoot),
						newTypeMeta("api.example.com", "v2", "Object", "", "", meta.RESTScopeRoot),
					},
				},
			},
			expectedMappingsByCluster: map[logicalcluster.Name]*DefaultRESTMapper{
				"one": newDefaultRESTMapperWith([]typeMeta{
					newTypeMeta("api.example.com", "v1", "Object", "", "", meta.RESTScopeRoot),
					newTypeMeta("api.example.com", "v2", "Object", "", "", meta.RESTScopeRoot),
				}),
			},
		},
		// Removing an existing entry and adding a new one should resolve into having only the new entry.
		// This could be triggered by e.g. deprecating an older version of a resource and adding a new one.
		{
			dmapper: &DynamicRESTMapper{
				byCluster: map[logicalcluster.Name]*DefaultRESTMapper{
					"one": newDefaultRESTMapperWith([]typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "", "", meta.RESTScopeRoot),
					}),
				},
			},
			applyPairs: map[logicalcluster.Name]applyPair{
				"one": {
					toRemove: []typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "", "", meta.RESTScopeRoot),
					},
					toAdd: []typeMeta{
						newTypeMeta("api.example.com", "v2", "Object", "", "", meta.RESTScopeRoot),
					},
				},
			},
			expectedMappingsByCluster: map[logicalcluster.Name]*DefaultRESTMapper{
				"one": newDefaultRESTMapperWith([]typeMeta{
					newTypeMeta("api.example.com", "v2", "Object", "", "", meta.RESTScopeRoot),
				}),
			},
		},
		// Removing all existing resources for a cluster should resolve to empty.
		{
			dmapper: &DynamicRESTMapper{
				byCluster: map[logicalcluster.Name]*DefaultRESTMapper{
					"one": newDefaultRESTMapperWith([]typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "", "", meta.RESTScopeRoot),
						newTypeMeta("api.example.com", "v2", "Object", "", "", meta.RESTScopeRoot),
					}),
				},
			},
			applyPairs: map[logicalcluster.Name]applyPair{
				"one": {
					toRemove: []typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "", "", meta.RESTScopeRoot),
						newTypeMeta("api.example.com", "v2", "Object", "", "", meta.RESTScopeRoot),
					},
				},
			},
			expectedMappingsByCluster: map[logicalcluster.Name]*DefaultRESTMapper{},
		},
		// Check that changes with more clusters are mapped correctly.
		{
			dmapper: &DynamicRESTMapper{
				byCluster: map[logicalcluster.Name]*DefaultRESTMapper{
					"one": newDefaultRESTMapperWith([]typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "", "", meta.RESTScopeRoot),
					}),
					"two": newDefaultRESTMapperWith([]typeMeta{
						newTypeMeta("api.example.com", "v2", "Object", "", "", meta.RESTScopeRoot),
					}),
				},
			},
			applyPairs: map[logicalcluster.Name]applyPair{
				"one": {
					toRemove: []typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "", "", meta.RESTScopeRoot),
					},
					toAdd: []typeMeta{
						newTypeMeta("api.example.com", "v2", "Object", "", "", meta.RESTScopeRoot),
					},
				},
				"two": {
					toRemove: []typeMeta{
						newTypeMeta("api.example.com", "v2", "Object", "", "", meta.RESTScopeRoot),
					},
					toAdd: []typeMeta{
						newTypeMeta("api.example.com", "v1", "Object", "", "", meta.RESTScopeRoot),
					},
				},
			},
			expectedMappingsByCluster: map[logicalcluster.Name]*DefaultRESTMapper{
				"one": newDefaultRESTMapperWith([]typeMeta{
					newTypeMeta("api.example.com", "v2", "Object", "", "", meta.RESTScopeRoot),
				}),
				"two": newDefaultRESTMapperWith([]typeMeta{
					newTypeMeta("api.example.com", "v1", "Object", "", "", meta.RESTScopeRoot),
				}),
			},
		},
	}

	for i, s := range scenarios {
		for clusterName, apply := range s.applyPairs {
			s.dmapper.ForCluster(clusterName).apply(apply.toRemove, apply.toAdd)
		}

		assert.Equal(t, s.expectedMappingsByCluster, s.dmapper.byCluster,
			"DynamicRESTMapper contains unexpected mapping", "case", i)
	}

	// Test use-before-create.

	objTypeMeta := newTypeMeta("api.example.com", "v1", "Object", "", "", meta.RESTScopeRoot)
	dmapper := NewDynamicRESTMapper(nil)
	oneMapper := dmapper.ForCluster("one")
	assert.NotNil(t, oneMapper, "DynamicRESTMapper.ForCluster() should never return nil")

	res, err := oneMapper.ResourceFor(objTypeMeta.groupVersionResourcePlural())
	assert.Equal(t, schema.GroupVersionResource{}, res,
		"ResourceFor() on an empty mapper should return empty result")
	assert.ErrorIs(t, err, &meta.NoResourceMatchError{},
		"ResourceFor() on an empty mapper should return an error of type NoResourceMatchError")

	// Test use-after-create.

	dmapper.ForCluster("one").apply(nil, []typeMeta{objTypeMeta})
	res, err = oneMapper.ResourceFor(objTypeMeta.groupVersionResourceSingular())
	assert.Nil(t, err,
		"ResourceFor() on match should not return an error")
	assert.Equal(t, objTypeMeta.groupVersionResourcePlural(), res,
		"ResourceFor() on match should return non-empty result")

	// Test use-after-delete.

	dmapper.ForCluster("one").apply([]typeMeta{objTypeMeta}, nil)
	res, err = oneMapper.ResourceFor(objTypeMeta.groupVersionResourceSingular())
	assert.Equal(t, schema.GroupVersionResource{}, res,
		"ResourceFor() on an empty mapper should return empty result")
	assert.ErrorIs(t, err, &meta.NoResourceMatchError{},
		"ResourceFor() on an empty mapper should return an error of type NoResourceMatchError")
}

func TestDiffResourceBindingsAnn(t *testing.T) {
	scenarios := []struct {
		oldAnn apibinding.ResourceBindingsAnnotation
		newAnn apibinding.ResourceBindingsAnnotation

		expectedToAdd    apibinding.ResourceBindingsAnnotation
		expectedToRemove apibinding.ResourceBindingsAnnotation
	}{
		// Only old.
		{
			oldAnn: apibinding.ResourceBindingsAnnotation{
				"1": {},
			},
			expectedToRemove: apibinding.ResourceBindingsAnnotation{
				"1": {},
			},
			expectedToAdd: make(apibinding.ResourceBindingsAnnotation),
		},
		// Only new.
		{
			newAnn: apibinding.ResourceBindingsAnnotation{
				"1": {},
			},
			expectedToRemove: make(apibinding.ResourceBindingsAnnotation),
			expectedToAdd: apibinding.ResourceBindingsAnnotation{
				"1": {},
			},
		},
		// Identical new and old annotations should cause no changes.
		{
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
		{
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
		{
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
		{
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

	for i, s := range scenarios {
		toRemove, toAdd :=
			diffResourceBindingsAnn(s.oldAnn, s.newAnn)
		assert.Equal(t, s.expectedToRemove, toRemove,
			"mismatch in annotation keys to remove", "case", i)
		assert.Equal(t, s.expectedToAdd, toAdd,
			"mismatch in annotation keys to add", "case", i)
	}
}
