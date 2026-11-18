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

// Package policyparams resolves the params of Validating- and MutatingAdmissionPolicies
// which live in logical clusters hosted on other shards.
package policyparams

import (
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
)

// KindResolver provides shard-local REST mapper which only knows the CRDs and
// the bound APIs of the logical clusters hosted on its own shard. A paramKind
// can be any resource served in the logical cluster of the policy though, so
// the group version of the paramKind is discovered in that logical cluster.
// Only that one group version is requested, and the result is shared by all
// users of a resolver.

const (
	// foundTTL is how long the resource of a kind is reused before it is discovered again.
	// Same as kube-apiserver.
	foundTTL = 30 * time.Second

	// notFoundTTL is how long a kind which could not be resolved is not looked up again.
	// Same as kube-apiserver.
	notFoundTTL = 5 * time.Second
)

// DiscoverFunc returns the resources a logical cluster serves for a group version, like
// discovery.ServerResourcesInterface.ServerResourcesForGroupVersion does for a single cluster.
type DiscoverFunc func(clusterName logicalcluster.Name, groupVersion string) (*metav1.APIResourceList, error)

// KindResolver resolves kinds to resources of logical clusters hosted on other shards.
type KindResolver struct {
	discover DiscoverFunc
	now      func() time.Time

	// fetches deduplicates concurrent discovery requests for the same group version of a logical cluster.
	fetches singleflight.Group

	lock          sync.Mutex
	groupVersions map[groupVersionKey]discoveredGroupVersion
}

type groupVersionKey struct {
	clusterName  logicalcluster.Name
	groupVersion schema.GroupVersion
}

type discoveredGroupVersion struct {
	// resources are nil if the group version is not served or could not be discovered.
	resources []metav1.APIResource
	// err is the error which prevented the discovery, if any.
	err error

	discoveredAt time.Time
}

// NewKindResolver returns a KindResolver which uses the given function for discovery.
func NewKindResolver(discover DiscoverFunc) *KindResolver {
	return &KindResolver{
		discover:      discover,
		now:           time.Now,
		groupVersions: map[groupVersionKey]discoveredGroupVersion{},
	}
}

// NewKindResolverForClient returns a KindResolver which discovers through the given client,
// which has to reach logical clusters on any shard.
func NewKindResolverForClient(kubeClusterClient kcpkubernetesclientset.ClusterInterface) *KindResolver {
	return NewKindResolver(func(clusterName logicalcluster.Name, groupVersion string) (*metav1.APIResourceList, error) {
		return kubeClusterClient.Cluster(clusterName.Path()).Discovery().ServerResourcesForGroupVersion(groupVersion)
	})
}

// RESTMapper returns a REST mapper for the given logical cluster, which is hosted on another shard.
//
// Kinds known to the given shard-local REST mapper, which are the built-in types that are the
// same in every logical cluster on every shard, are resolved by it. All other kinds are resolved
// by discovery in the logical cluster, but only through RESTMapping and RESTMappings, which is
// what admission policies resolve a paramKind with.
func (r *KindResolver) RESTMapper(clusterName logicalcluster.Name, local meta.RESTMapper) meta.RESTMapper {
	return &restMapper{
		RESTMapper:  local,
		clusterName: clusterName,
		resolver:    r,
	}
}

type restMapper struct {
	meta.RESTMapper

	clusterName logicalcluster.Name
	resolver    *KindResolver
}

func (m *restMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	mappings, err := m.RESTMappings(gk, versions...)
	if err != nil {
		return nil, err
	}
	return mappings[0], nil
}

func (m *restMapper) RESTMappings(gk schema.GroupKind, versions ...string) ([]*meta.RESTMapping, error) {
	mappings, err := m.RESTMapper.RESTMappings(gk, versions...)
	if err == nil || !meta.IsNoMatchError(err) {
		return mappings, err
	}

	for _, version := range versions {
		mapping, err := m.resolver.resolve(m.clusterName, gk.WithVersion(version))
		if err != nil {
			return nil, err
		}
		if mapping != nil {
			mappings = append(mappings, mapping)
		}
	}
	if len(mappings) == 0 {
		return nil, &meta.NoKindMatchError{GroupKind: gk, SearchedVersions: versions}
	}
	return mappings, nil
}

// resolve returns the mapping of the kind in the logical cluster, or nil if the logical cluster does not serve it.
func (r *KindResolver) resolve(clusterName logicalcluster.Name, gvk schema.GroupVersionKind) (*meta.RESTMapping, error) {
	key := groupVersionKey{clusterName: clusterName, groupVersion: gvk.GroupVersion()}

	r.lock.Lock()
	cached, discovered := r.groupVersions[key]
	r.lock.Unlock()

	if discovered {
		mapping := cached.mappingFor(gvk)
		ttl := notFoundTTL
		if mapping != nil {
			ttl = foundTTL
		}
		if r.now().Sub(cached.discoveredAt) < ttl {
			return mapping, cached.err
		}
	}

	fetched, _, _ := r.fetches.Do(clusterName.String()+"/"+key.groupVersion.String(), func() (any, error) {
		fresh := discoveredGroupVersion{discoveredAt: r.now()}
		resourceList, err := r.discover(clusterName, key.groupVersion.String())
		switch {
		case err == nil:
			fresh.resources = resourceList.APIResources
		case apierrors.IsNotFound(err):
			// the logical cluster does not serve the group version
		case cached.resources != nil:
			// The other shard could not be asked. Keep what is known about the group version
			// instead of failing admission requests which worked until now.
			fresh.resources = cached.resources
		default:
			fresh.err = err
		}

		r.lock.Lock()
		defer r.lock.Unlock()
		// The logical clusters are hosted on other shards, so their deletion is not observed here.
		// Drop what was not looked up for a while instead.
		for otherKey, other := range r.groupVersions {
			if fresh.discoveredAt.Sub(other.discoveredAt) >= 2*foundTTL {
				delete(r.groupVersions, otherKey)
			}
		}
		r.groupVersions[key] = fresh

		return fresh, nil
	})
	fresh := fetched.(discoveredGroupVersion)

	return fresh.mappingFor(gvk), fresh.err
}

func (d discoveredGroupVersion) mappingFor(gvk schema.GroupVersionKind) *meta.RESTMapping {
	for _, resource := range d.resources {
		if resource.Kind != gvk.Kind || strings.Contains(resource.Name, "/") {
			// not the kind, or a subresource of it
			continue
		}

		var scope meta.RESTScope = meta.RESTScopeRoot
		if resource.Namespaced {
			scope = meta.RESTScopeNamespace
		}
		return &meta.RESTMapping{
			Resource:         gvk.GroupVersion().WithResource(resource.Name),
			GroupVersionKind: gvk,
			Scope:            scope,
		}
	}
	return nil
}
