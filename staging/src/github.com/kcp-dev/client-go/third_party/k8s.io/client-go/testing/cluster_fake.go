/*
Copyright 2022 The Kubernetes Authors.

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

package testing

import (
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	restclient "k8s.io/client-go/rest"
)

type GenericReactor[R any] interface {
	// Handles indicates whether or not this Reactor deals with a given
	// action.
	Handles(action Action) bool
	// React handles the action and returns results.  It may choose to
	// delegate by indicated handled=false.
	React(action Action) (handled bool, ret R, err error)
}

type ReactorPredicate[R any] struct {
	predicate func(Action) bool
	delegate  GenericReactor[R]
}

func (r *ReactorPredicate[R]) Handles(action Action) bool {
	return r.predicate(action) && r.delegate.Handles(action)
}

func (r *ReactorPredicate[R]) React(action Action) (handled bool, ret R, err error) {
	return r.delegate.React(action)
}

// ClusterScopedReactor wraps any reactor to scope it to the events from a particular cluster.
func ClusterScopedReactor[R any](clusterPath logicalcluster.Path, reactor GenericReactor[R]) GenericReactor[R] {
	return &ReactorPredicate[R]{
		predicate: func(action Action) bool {
			return action.GetCluster() == clusterPath
		},
		delegate: reactor,
	}
}

// AddScopedReactor appends a reactor to the end of the chain.
func (c *Fake) AddScopedReactor(clusterPath logicalcluster.Path, verb, resource string, reaction ReactionFunc) {
	c.ReactionChain = append(c.ReactionChain, ClusterScopedReactor[runtime.Object](clusterPath, &SimpleReactor{verb, resource, reaction}))
}

// PrependScopedReactor adds a reactor to the beginning of the chain.
func (c *Fake) PrependScopedReactor(clusterPath logicalcluster.Path, verb, resource string, reaction ReactionFunc) {
	c.ReactionChain = append([]Reactor{ClusterScopedReactor[runtime.Object](clusterPath, &SimpleReactor{verb, resource, reaction})}, c.ReactionChain...)
}

// AddScopedWatchReactor appends a reactor to the end of the chain.
func (c *Fake) AddScopedWatchReactor(clusterPath logicalcluster.Path, resource string, reaction WatchReactionFunc) {
	c.Lock()
	defer c.Unlock()
	c.WatchReactionChain = append(c.WatchReactionChain, ClusterScopedReactor[watch.Interface](clusterPath, &SimpleWatchReactor{resource, reaction}))
}

// PrependScopedWatchReactor adds a reactor to the beginning of the chain.
func (c *Fake) PrependScopedWatchReactor(clusterPath logicalcluster.Path, resource string, reaction WatchReactionFunc) {
	c.Lock()
	defer c.Unlock()
	c.WatchReactionChain = append([]WatchReactor{ClusterScopedReactor[watch.Interface](clusterPath, &SimpleWatchReactor{resource, reaction})}, c.WatchReactionChain...)
}

// AddScopedProxyReactor appends a reactor to the end of the chain.
func (c *Fake) AddScopedProxyReactor(clusterPath logicalcluster.Path, resource string, reaction ProxyReactionFunc) {
	c.ProxyReactionChain = append(c.ProxyReactionChain, ClusterScopedReactor[restclient.ResponseWrapper](clusterPath, &SimpleProxyReactor{resource, reaction}))
}

// PrependScopedProxyReactor adds a reactor to the beginning of the chain.
func (c *Fake) PrependScopedProxyReactor(clusterPath logicalcluster.Path, resource string, reaction ProxyReactionFunc) {
	c.ProxyReactionChain = append([]ProxyReactor{ClusterScopedReactor[restclient.ResponseWrapper](clusterPath, &SimpleProxyReactor{resource, reaction})}, c.ProxyReactionChain...)
}
