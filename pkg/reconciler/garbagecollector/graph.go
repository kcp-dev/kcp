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

package garbagecollector

import (
	"slices"

	"k8s.io/utils/ptr"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/reconciler/garbagecollector/syncmap"
)

type Graph struct {
	// global is a map of objects to its owned objects across all clusters.
	//
	// The value is a pointer to a slice because the value must be
	// comparable for the syncmap to be able to do atomic operations on
	// it. That also means that when modifying the slice, a new slice
	// must be created.
	global *syncmap.SyncMap[ID, *[]ObjectReference]
}

func NewGraph() *Graph {
	g := &Graph{}
	g.global = syncmap.NewSyncMap[ID, *[]ObjectReference]()
	return g
}

// ClusterOwned returns all object references owned by objects in the given cluster.
func (g *Graph) ClusterOwned(clusterName logicalcluster.Name) []ObjectReference {
	var allDependents []ObjectReference
	g.global.Range(func(id ID, deps *[]ObjectReference) bool {
		if id.ClusterName == clusterName {
			allDependents = append(allDependents, *deps...)
		}
		return true
	})
	return allDependents
}

// RemoveCluster removes the given cluster from the graph.
// It returns true if the cluster was removed or not present.
// If returned false the cluster had objects with owned objects, and the owned objects are returned.
func (g *Graph) RemoveCluster(clusterName logicalcluster.Name) (bool, []ObjectReference) {
	owned := g.ClusterOwned(clusterName)
	if len(owned) > 0 {
		return false, owned
	}
	// TODO this function might not be necessary.
	return true, nil
}

// Add adds the given object and updates its owners in the graph.
func (g *Graph) Add(obj ObjectReference, oldOwners, newOwners []ObjectReference) {
	// Store the object.
	g.global.StoreIfAbsent(obj.ID(), ptr.To([]ObjectReference{}))

	// TODO diff oldOwners and newOwners to avoid unnecessary
	// modifications.

	for _, owner := range oldOwners {
		g.global.Modify(owner.ID(), func(deps *[]ObjectReference, exists bool) *[]ObjectReference {
			if !exists || deps == nil {
				deps = ptr.To([]ObjectReference{})
			}
			newDeps := slices.DeleteFunc(*deps, func(dep ObjectReference) bool {
				return dep.Equals(obj)
			})
			return &newDeps
		})
	}

	for _, owner := range newOwners {
		g.global.Modify(owner.ID(), func(deps *[]ObjectReference, exists bool) *[]ObjectReference {
			return ptr.To(
				append(
					ptr.Deref(deps, []ObjectReference{}),
					obj,
				),
			)
		})
	}
}

func (g *Graph) Owned(or ObjectReference) []ObjectReference {
	ownedObjs, _ := g.global.Load(or.ID())
	if ownedObjs == nil {
		return []ObjectReference{}
	}
	return *ownedObjs
}

// Remove removes the given object from the graph.
// It returns true if the object was removed or is not present.
// If the object owns objects it returns false and the owned objects.
func (g *Graph) Remove(or ObjectReference) (bool, []ObjectReference) {
	ownedObjs, exists := g.global.Load(or.ID())
	// TODO(ntnn): This could be an error. Depends on if "empty" objects
	// (so nodes not owning anything) are expected to be in the graph or
	// not.
	if !exists {
		return true, nil
	}
	if len(*ownedObjs) > 0 {
		return false, *ownedObjs
	}
	g.global.Delete(or.ID())
	g.global.Range(func(ownerID ID, ownedObjs *[]ObjectReference) bool {
		g.global.Modify(ownerID, func(ownedObjs *[]ObjectReference, _ bool) *[]ObjectReference {
			newOwnedObjs := slices.DeleteFunc(*ownedObjs, func(ownedObj ObjectReference) bool {
				return ownedObj.ID() == or.ID()
			})
			if slices.Equal(*ownedObjs, newOwnedObjs) {
				return ownedObjs
			}
			return &newOwnedObjs
		})
		return true
	})
	return true, nil
}
