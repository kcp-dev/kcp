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

package policyparams

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/logicalcluster/v3"
)

const policyCluster = logicalcluster.Name("policy-cluster")

var (
	configMapKind = schema.GroupKind{Kind: "ConfigMap"}
	sheriffKind   = schema.GroupKind{Group: "wildwest.dev", Kind: "Sheriff"}
	cowboyKind    = schema.GroupKind{Group: "wildwest.dev", Kind: "Cowboy"}

	wildwestResources = &metav1.APIResourceList{
		GroupVersion: "wildwest.dev/v1alpha1",
		APIResources: []metav1.APIResource{
			{Name: "sheriffs/status", Kind: "Sheriff"},
			{Name: "sheriffs", Kind: "Sheriff", Namespaced: false},
			{Name: "cowboys", Kind: "Cowboy", Namespaced: true},
		},
	}
)

// fakeShard is the other shard, on which the logical cluster of the policy is hosted.
type fakeShard struct {
	t *testing.T

	resources *metav1.APIResourceList
	err       error

	requests []string
}

func (s *fakeShard) discover(clusterName logicalcluster.Name, groupVersion string) (*metav1.APIResourceList, error) {
	require.Equal(s.t, policyCluster, clusterName)
	s.requests = append(s.requests, groupVersion)

	if s.err != nil {
		return nil, s.err
	}
	if s.resources == nil {
		return nil, apierrors.NewNotFound(schema.GroupResource{}, groupVersion)
	}
	return s.resources, nil
}

// newTestRESTMapper returns a REST mapper for the policy cluster, the other shard it
// discovers the kinds on, and a function to let time pass.
func newTestRESTMapper(t *testing.T) (meta.RESTMapper, *fakeShard, func(time.Duration)) {
	t.Helper()

	// like the shard-local REST mapper, which knows the built-in types of every logical cluster
	local := meta.NewDefaultRESTMapper(nil)
	local.Add(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, meta.RESTScopeNamespace)

	shard := &fakeShard{t: t}
	resolver := NewKindResolver(shard.discover)

	now := time.Now()
	resolver.now = func() time.Time { return now }

	return resolver.RESTMapper(policyCluster, local), shard, func(d time.Duration) { now = now.Add(d) }
}

func TestRESTMapperResolvesBuiltInKindsLocally(t *testing.T) {
	t.Parallel()

	restMapper, shard, _ := newTestRESTMapper(t)

	mapping, err := restMapper.RESTMapping(configMapKind, "v1")
	require.NoError(t, err)
	require.Equal(t, schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, mapping.Resource)
	require.Equal(t, meta.RESTScopeNameNamespace, mapping.Scope.Name())

	require.Empty(t, shard.requests, "built-in kinds must not be discovered on the other shard")
}

func TestRESTMapperDiscoversOtherKinds(t *testing.T) {
	t.Parallel()

	restMapper, shard, _ := newTestRESTMapper(t)
	shard.resources = wildwestResources

	mapping, err := restMapper.RESTMapping(sheriffKind, "v1alpha1")
	require.NoError(t, err)
	require.Equal(t, schema.GroupVersionResource{Group: "wildwest.dev", Version: "v1alpha1", Resource: "sheriffs"}, mapping.Resource)
	require.Equal(t, sheriffKind.WithVersion("v1alpha1"), mapping.GroupVersionKind)
	require.Equal(t, meta.RESTScopeNameRoot, mapping.Scope.Name())

	mapping, err = restMapper.RESTMapping(cowboyKind, "v1alpha1")
	require.NoError(t, err)
	require.Equal(t, "cowboys", mapping.Resource.Resource)
	require.Equal(t, meta.RESTScopeNameNamespace, mapping.Scope.Name())

	require.Equal(t, []string{"wildwest.dev/v1alpha1"}, shard.requests, "only the group version of the kind must be discovered, and only once")
}

func TestRESTMapperRefreshesFoundKinds(t *testing.T) {
	t.Parallel()

	restMapper, shard, wait := newTestRESTMapper(t)
	shard.resources = wildwestResources

	_, err := restMapper.RESTMapping(sheriffKind, "v1alpha1")
	require.NoError(t, err)

	wait(foundTTL - time.Second)
	_, err = restMapper.RESTMapping(sheriffKind, "v1alpha1")
	require.NoError(t, err)
	require.Len(t, shard.requests, 1)

	// the kind is served as a namespaced resource now
	shard.resources = &metav1.APIResourceList{APIResources: []metav1.APIResource{{Name: "sheriffs", Kind: "Sheriff", Namespaced: true}}}

	wait(time.Second)
	mapping, err := restMapper.RESTMapping(sheriffKind, "v1alpha1")
	require.NoError(t, err)
	require.Equal(t, meta.RESTScopeNameNamespace, mapping.Scope.Name())
	require.Len(t, shard.requests, 2)
}

func TestRESTMapperFindsKindsCreatedLater(t *testing.T) {
	t.Parallel()

	for title, served := range map[string]*metav1.APIResourceList{
		"group version is not served yet":                 nil,
		"group version is served without the kind so far": {APIResources: []metav1.APIResource{{Name: "cowboys", Kind: "Cowboy", Namespaced: true}}},
	} {
		t.Run(title, func(t *testing.T) {
			t.Parallel()

			restMapper, shard, wait := newTestRESTMapper(t)
			shard.resources = served

			_, err := restMapper.RESTMapping(sheriffKind, "v1alpha1")
			require.True(t, meta.IsNoMatchError(err), "expected a no match error, got: %v", err)

			// the CRD of the kind is created
			shard.resources = wildwestResources

			// A policy with an unknown paramKind is retried every second. That must neither
			// query the other shard every time, nor take long to find the kind.
			wait(notFoundTTL - time.Second)
			_, err = restMapper.RESTMapping(sheriffKind, "v1alpha1")
			require.True(t, meta.IsNoMatchError(err), "expected a no match error, got: %v", err)
			require.Len(t, shard.requests, 1)

			wait(time.Second)
			mapping, err := restMapper.RESTMapping(sheriffKind, "v1alpha1")
			require.NoError(t, err)
			require.Equal(t, "sheriffs", mapping.Resource.Resource)
			require.Len(t, shard.requests, 2)
		})
	}
}

func TestRESTMapperDiscoveryErrors(t *testing.T) {
	t.Parallel()

	t.Run("an unknown kind is not resolved and retried", func(t *testing.T) {
		t.Parallel()

		restMapper, shard, wait := newTestRESTMapper(t)
		shard.err = fmt.Errorf("other shard is not reachable")

		_, err := restMapper.RESTMapping(sheriffKind, "v1alpha1")
		require.ErrorContains(t, err, "other shard is not reachable")
		require.False(t, meta.IsNoMatchError(err), "an error of the discovery must not look like the kind does not exist")

		_, err = restMapper.RESTMapping(sheriffKind, "v1alpha1")
		require.ErrorContains(t, err, "other shard is not reachable")
		require.Len(t, shard.requests, 1, "the other shard must not be queried on every retry")

		shard.err, shard.resources = nil, wildwestResources
		wait(notFoundTTL)
		_, err = restMapper.RESTMapping(sheriffKind, "v1alpha1")
		require.NoError(t, err)
	})

	t.Run("a known kind stays resolved", func(t *testing.T) {
		t.Parallel()

		restMapper, shard, wait := newTestRESTMapper(t)
		shard.resources = wildwestResources

		_, err := restMapper.RESTMapping(sheriffKind, "v1alpha1")
		require.NoError(t, err)

		shard.err = fmt.Errorf("other shard is not reachable")
		wait(foundTTL)
		mapping, err := restMapper.RESTMapping(sheriffKind, "v1alpha1")
		require.NoError(t, err)
		require.Equal(t, "sheriffs", mapping.Resource.Resource)
		require.Len(t, shard.requests, 2)
	})
}

func TestRESTMapperForgetsUnusedLogicalClusters(t *testing.T) {
	t.Parallel()

	shard := &fakeShard{t: t, resources: wildwestResources}
	resolver := NewKindResolver(func(_ logicalcluster.Name, groupVersion string) (*metav1.APIResourceList, error) {
		return shard.discover(policyCluster, groupVersion)
	})
	now := time.Now()
	resolver.now = func() time.Time { return now }
	local := meta.NewDefaultRESTMapper(nil)

	_, err := resolver.RESTMapper("deleted-cluster", local).RESTMapping(sheriffKind, "v1alpha1")
	require.NoError(t, err)
	require.Len(t, resolver.groupVersions, 1)

	now = now.Add(2 * foundTTL)
	_, err = resolver.RESTMapper(policyCluster, local).RESTMapping(sheriffKind, "v1alpha1")
	require.NoError(t, err)
	require.Len(t, resolver.groupVersions, 1)
	require.Contains(t, resolver.groupVersions, groupVersionKey{clusterName: policyCluster, groupVersion: sheriffKind.WithVersion("v1alpha1").GroupVersion()})
}
