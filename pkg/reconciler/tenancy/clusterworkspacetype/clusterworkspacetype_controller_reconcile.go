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

package clusterworkspacetype

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/kcp-dev/logicalcluster"
	"gonum.org/v1/gonum/graph"
	"gonum.org/v1/gonum/graph/simple"
	"gonum.org/v1/gonum/graph/topo"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	virtualworkspacesoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	"github.com/kcp-dev/kcp/pkg/virtual/initializingworkspaces"
)

func (c *controller) reconcile(ctx context.Context, cwt *tenancyv1alpha1.ClusterWorkspaceType) {
	if err := c.updateVirtualWorkspaceURLs(cwt); err != nil {
		conditions.MarkFalse(
			cwt,
			tenancyv1alpha1.ClusterWorkspaceTypeVirtualWorkspaceURLsReady,
			tenancyv1alpha1.ErrorGeneratingURLsReason,
			conditionsv1alpha1.ConditionSeverityError,
			err.Error(),
		)
	} else {
		conditions.MarkTrue(
			cwt,
			tenancyv1alpha1.ClusterWorkspaceTypeVirtualWorkspaceURLsReady,
		)
	}

	if err := c.resolveTypeExtensions(cwt); err != nil {
		conditions.MarkFalse(
			cwt,
			tenancyv1alpha1.ClusterWorkspaceTypeExtensionsResolved,
			tenancyv1alpha1.ErrorResolvingExtensionsReason,
			conditionsv1alpha1.ConditionSeverityError,
			err.Error(),
		)
	} else {
		conditions.MarkTrue(
			cwt,
			tenancyv1alpha1.ClusterWorkspaceTypeExtensionsResolved,
		)
	}

	if err := c.resolveTypeRelationships(cwt); err != nil {
		conditions.MarkFalse(
			cwt,
			tenancyv1alpha1.ClusterWorkspaceTypeRelationshipsValid,
			tenancyv1alpha1.ErrorValidatingRelationshipsReason,
			conditionsv1alpha1.ConditionSeverityError,
			err.Error(),
		)
	} else {
		conditions.MarkTrue(
			cwt,
			tenancyv1alpha1.ClusterWorkspaceTypeRelationshipsValid,
		)
	}

	conditions.SetSummary(cwt)
}

func (c *controller) updateVirtualWorkspaceURLs(cwt *tenancyv1alpha1.ClusterWorkspaceType) error {
	clusterWorkspaceShards, err := c.listClusterWorkspaceShards()
	if err != nil {
		return fmt.Errorf("error listing ClusterWorkspaceShards: %w", err)
	}

	desiredURLs := sets.NewString()
	for _, clusterWorkspaceShard := range clusterWorkspaceShards {
		if clusterWorkspaceShard.Spec.ExternalURL == "" {
			continue
		}

		u, err := url.Parse(clusterWorkspaceShard.Spec.ExternalURL)
		if err != nil {
			// Should never happen
			klog.Errorf(
				"Error parsing ClusterWorkspaceShard %s|%s spec.externalURL %q: %v",
				logicalcluster.From(clusterWorkspaceShard),
				clusterWorkspaceShard.Name,
				clusterWorkspaceShard.Spec.ExternalURL,
			)

			continue
		}

		u.Path = path.Join(
			u.Path,
			virtualworkspacesoptions.DefaultRootPathPrefix,
			initializingworkspaces.VirtualWorkspaceName,
			string(initialization.InitializerForType(cwt)),
		)

		desiredURLs.Insert(u.String())
	}

	cwt.Status.VirtualWorkspaces = nil

	for _, u := range desiredURLs.List() {
		cwt.Status.VirtualWorkspaces = append(cwt.Status.VirtualWorkspaces, tenancyv1alpha1.VirtualWorkspace{
			URL: u,
		})
	}

	return nil
}

type graphContext struct {
	// while we're building the graph, we need to resolve types
	resolveClusterWorkspaceType func(reference tenancyv1alpha1.ClusterWorkspaceTypeReference) (*tenancyv1alpha1.ClusterWorkspaceType, error)

	// we need to be able to jump into the correct node in the graph based on the CWT
	nodeIdByRef map[tenancyv1alpha1.ClusterWorkspaceTypeReference]int64

	// this is the graph we're operating on during traversals
	directedGraph *simple.DirectedGraph
}

type edge struct {
	graph.Edge
	relationship edgeRelationship
}

type edgeRelationship int

const (
	with edgeRelationship = iota
	without
)

// resolveTypeExtensions walks ClusterWorkspaceType extension references to resolve the set of
// initializers and aliases that describe the type cwt
func (c *controller) resolveTypeExtensions(cwt *tenancyv1alpha1.ClusterWorkspaceType) error {
	gCtx := graphContext{
		resolveClusterWorkspaceType: c.resolveClusterWorkspaceType,
		nodeIdByRef:                 map[tenancyv1alpha1.ClusterWorkspaceTypeReference]int64{},
		directedGraph:               simple.NewDirectedGraph(),
	}

	// first, resolve the dependencies of this type and its extensions
	if err := gCtx.add(cwt); err != nil {
		return err
	}

	// then, ensure that there are no cyclical relationships
	if err := gCtx.validateAcyclic(); err != nil {
		return err
	}

	// finally, resolve the initializers and aliases for each type
	gCtx.resolve(cwt)
	return nil
}

type node struct {
	graph.Node
	cwt *tenancyv1alpha1.ClusterWorkspaceType
}

// add the node and edges for this type, recursively handling outward references as necessary
func (g *graphContext) add(cwt *tenancyv1alpha1.ClusterWorkspaceType) error {
	ref := tenancyv1alpha1.ReferenceFor(cwt)
	if _, previouslyAdded := g.nodeIdByRef[ref]; previouslyAdded {
		return nil // we hit this when we have a cycle
	}

	// add a node for this type
	from := &node{
		Node: g.directedGraph.NewNode(),
		cwt:  cwt,
	}
	id := from.ID()
	g.nodeIdByRef[ref] = id
	g.directedGraph.AddNode(from)

	// ensure the correct edges leave this node
	for relationship, references := range map[edgeRelationship][]tenancyv1alpha1.ClusterWorkspaceTypeReference{
		with:    cwt.Spec.Extend.With,
		without: cwt.Spec.Extend.Without,
	} {
		for _, reference := range references {
			toType, err := g.resolveClusterWorkspaceType(reference)
			if err != nil {
				return err
			}

			if err := g.add(toType); err != nil {
				return err
			}

			to := g.directedGraph.Node(g.nodeIdByRef[reference])
			// this particular library doesn't allow a self-edge, and panics if that occurs -
			// we should expose nicer behavior to the end user
			if from.ID() == to.ID() {
				return errors.New("cannot use a self-reference during type extension")
			}

			g.directedGraph.SetEdge(&edge{
				Edge:         g.directedGraph.NewEdge(from, to),
				relationship: relationship,
			})
		}
	}
	return nil
}

func (g *graphContext) validateAcyclic() error {
	if cycles := topo.DirectedCyclesIn(g.directedGraph); len(cycles) != 0 {
		var formattedCycles []string
		for _, cycle := range cycles {
			var involved []string
			for _, item := range cycle {
				involved = append(involved, tenancyv1alpha1.ReferenceFor(item.(*node).cwt).String())
			}
			formattedCycles = append(formattedCycles, fmt.Sprintf("[%s]", strings.Join(involved, ", ")))
		}

		noun := "a cycle"
		if len(cycles) > 1 {
			noun = "cycles"
		}

		sort.Strings(formattedCycles)
		return fmt.Errorf("type extension creates %s: %v", noun, strings.Join(formattedCycles, ", "))
	}
	return nil
}

// the Visit() hook on the graph traversal's DFS comes *before* visiting child nodes, instead of after,
// which mostly invalidates the reason you'd use the DFS in the first place and means that we need to
// implement our own version here rather than use theirs. They also hide their stack implementation
// from us, so it's not simple to just adapt their DFS and move the hook, so we use a recursive version.
func (g *graphContext) resolve(cwt *tenancyv1alpha1.ClusterWorkspaceType) {
	withInitializers, withoutInitializers := initializerSet{}, initializerSet{}
	withAliases, withoutAliases := referenceSet{}, referenceSet{}

	ref := tenancyv1alpha1.ReferenceFor(cwt)
	n := g.directedGraph.Node(g.nodeIdByRef[ref])
	others := g.directedGraph.From(n.ID())
	for others.Next() {
		other := others.Node()

		var intoInitializers initializerSet
		var intoAliases referenceSet

		switch g.directedGraph.Edge(n.ID(), other.ID()).(*edge).relationship {
		case with:
			intoInitializers = withInitializers
			intoAliases = withAliases
		case without:
			intoInitializers = withoutInitializers
			intoAliases = withoutAliases
		}

		otherCwt := other.(*node).cwt
		g.resolve(otherCwt)
		intoInitializers.Insert(otherCwt.Status.Initializers...)
		intoAliases.Insert(otherCwt.Status.TypeAliases...)
	}

	initializers := withInitializers.Difference(withoutInitializers)
	if cwt.Spec.Initializer {
		initializers.Insert(initialization.InitializerForType(cwt))
	}
	cwt.Status.Initializers = initializers.List()

	aliases := referenceSet{}
	for alias := range withAliases {
		other := g.directedGraph.Node(g.nodeIdByRef[alias])
		if newReferenceSet(other.(*node).cwt.Status.TypeAliases...).HasAny(withoutAliases.UnsortedList()...) {
			continue
		}
		if withoutAliases.Has(alias) {
			continue
		}
		aliases.Insert(alias)
	}
	aliases.Insert(ref)
	cwt.Status.TypeAliases = aliases.List()
}

// initializerSet is a set of initializers, implemented via map[string]struct{} for minimal memory consumption.
type initializerSet map[tenancyv1alpha1.ClusterWorkspaceInitializer]sets.Empty

// Insert adds items to the set.
func (s initializerSet) Insert(items ...tenancyv1alpha1.ClusterWorkspaceInitializer) initializerSet {
	for _, item := range items {
		s[item] = sets.Empty{}
	}
	return s
}

// Has returns true if and only if item is contained in the set.
func (s initializerSet) Has(item tenancyv1alpha1.ClusterWorkspaceInitializer) bool {
	_, contained := s[item]
	return contained
}

// Difference returns a set of objects that are not in s2
// For example:
// s1 = {a1, a2, a3}
// s2 = {a1, a2, a4, a5}
// s1.Difference(s2) = {a3}
// s2.Difference(s1) = {a4, a5}
func (s initializerSet) Difference(s2 initializerSet) initializerSet {
	result := initializerSet{}
	for key := range s {
		if !s2.Has(key) {
			result.Insert(key)
		}
	}
	return result
}

// List returns the contents as a sorted string slice.
func (s initializerSet) List() []tenancyv1alpha1.ClusterWorkspaceInitializer {
	res := make([]tenancyv1alpha1.ClusterWorkspaceInitializer, 0, len(s))
	for key := range s {
		res = append(res, key)
	}
	sort.Slice(res, func(i, j int) bool {
		return res[i] < res[j]
	})
	return res
}

// newReferenceSet creates a referenceSet from a list of values.
func newReferenceSet(items ...tenancyv1alpha1.ClusterWorkspaceTypeReference) referenceSet {
	ss := referenceSet{}
	ss.Insert(items...)
	return ss
}

// referenceSet is a set of references, implemented via map[string]struct{} for minimal memory consumption.
type referenceSet map[tenancyv1alpha1.ClusterWorkspaceTypeReference]sets.Empty

// Insert adds items to the set.
func (s referenceSet) Insert(items ...tenancyv1alpha1.ClusterWorkspaceTypeReference) referenceSet {
	for _, item := range items {
		s[item] = sets.Empty{}
	}
	return s
}

// Has returns true if and only if item is contained in the set.
func (s referenceSet) Has(item tenancyv1alpha1.ClusterWorkspaceTypeReference) bool {
	_, contained := s[item]
	return contained
}

// HasAny returns true if any items are contained in the set.
func (s referenceSet) HasAny(items ...tenancyv1alpha1.ClusterWorkspaceTypeReference) bool {
	for _, item := range items {
		if s.Has(item) {
			return true
		}
	}
	return false
}

// Difference returns a set of objects that are not in s2
// For example:
// s1 = {a1, a2, a3}
// s2 = {a1, a2, a4, a5}
// s1.Difference(s2) = {a3}
// s2.Difference(s1) = {a4, a5}
func (s referenceSet) Difference(s2 referenceSet) referenceSet {
	result := referenceSet{}
	for key := range s {
		if !s2.Has(key) {
			result.Insert(key)
		}
	}
	return result
}

// List returns the contents as a sorted string slice.
func (s referenceSet) List() []tenancyv1alpha1.ClusterWorkspaceTypeReference {
	res := make([]tenancyv1alpha1.ClusterWorkspaceTypeReference, 0, len(s))
	for key := range s {
		res = append(res, key)
	}
	sort.Slice(res, func(i, j int) bool {
		if res[i].Path == res[j].Path {
			return res[i].Name < res[j].Name
		}
		return res[i].Path < res[j].Path
	})
	return res
}

// UnsortedList returns the slice with contents in random order.
func (s referenceSet) UnsortedList() []tenancyv1alpha1.ClusterWorkspaceTypeReference {
	res := make([]tenancyv1alpha1.ClusterWorkspaceTypeReference, 0, len(s))
	for key := range s {
		res = append(res, key)
	}
	return res
}

// resolveTypeRelationships ensures that:
// - the default child type and allowed child types allow this owning type as a parent
//   - the default child type is an allowed child type
// - the allowed parent types allow this owning type as a child
func (c *controller) resolveTypeRelationships(cwt *tenancyv1alpha1.ClusterWorkspaceType) error {
	if !conditions.IsTrue(cwt, tenancyv1alpha1.ClusterWorkspaceTypeExtensionsResolved) {
		return fmt.Errorf("type extensions have not yet been resolved")
	}

	if cwt.Spec.DefaultChildWorkspaceType != nil &&
		!cwt.Spec.AllowAnyChildWorkspaceTypes &&
		!newReferenceSet(cwt.Spec.AllowedChildWorkspaceTypes...).Has(*cwt.Spec.DefaultChildWorkspaceType) {
		return fmt.Errorf("default child type %s is not allowed as a child", cwt.Spec.DefaultChildWorkspaceType.String())
	}

	ref := tenancyv1alpha1.ReferenceFor(cwt)

	// these are the types which, if they are present in some other type's list, allow us
	ourTypes := newReferenceSet(cwt.Status.TypeAliases...)

	for _, child := range cwt.Spec.AllowedChildWorkspaceTypes {
		childType, err := c.resolveClusterWorkspaceType(child)
		if err != nil {
			return fmt.Errorf("could not resolve child type %s: %w", child, err)
		}
		allowedParents := newReferenceSet(childType.Spec.AllowedParentWorkspaceTypes...)
		if !childType.Spec.AllowAnyParentWorkspaceTypes && !allowedParents.HasAny(ourTypes.List()...) {
			return fmt.Errorf("child %s does not allow %s as a parent", child, ref)
		}
	}

	for _, parent := range cwt.Spec.AllowedParentWorkspaceTypes {
		parentType, err := c.resolveClusterWorkspaceType(parent)
		if err != nil {
			return fmt.Errorf("could not resolve parent type %s: %w", parent, err)
		}
		allowedChildren := newReferenceSet(parentType.Spec.AllowedChildWorkspaceTypes...)
		if !parentType.Spec.AllowAnyChildWorkspaceTypes && !allowedChildren.HasAny(ourTypes.List()...) {
			return fmt.Errorf("parent %s does not allow %s as a child", parent, ref)
		}
	}

	return nil
}
