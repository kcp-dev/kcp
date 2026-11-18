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

// Package shardclient provides clients which connect directly to the shard hosting a logical cluster.
package shardclient

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"k8s.io/client-go/rest"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	corev1alpha1listers "github.com/kcp-dev/sdk/client/listers/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/indexers"
)

// BaseURLFunc returns the base URL of the shard which hosts a logical cluster.
type BaseURLFunc func(clusterName logicalcluster.Name) (*url.URL, error)

// WithDirectRouting returns a copy of the config, whose clients send each request directly to the
// shard hosting the logical cluster of the request, instead of to the host of the config.
//
// The requests have to address the logical cluster by its name, not by a workspace path. The
// credentials of the config have to be accepted by every shard.
func WithDirectRouting(config *rest.Config, baseURLForCluster BaseURLFunc) *rest.Config {
	config = rest.CopyConfig(config)
	config.Wrap(func(delegate http.RoundTripper) http.RoundTripper {
		return &routingRoundTripper{
			delegate:          delegate,
			baseURLForCluster: baseURLForCluster,
		}
	})
	return config
}

// BaseURLFromCache returns a BaseURLFunc which looks up the shard of a logical cluster and its
// base URL in the given listers, which have to be backed by the cache server to know the
// logical clusters and shards of the whole installation.
func BaseURLFromCache(logicalClusterLister corev1alpha1listers.LogicalClusterClusterLister, shardLister corev1alpha1listers.ShardClusterLister) BaseURLFunc {
	return func(clusterName logicalcluster.Name) (*url.URL, error) {
		logicalCluster, err := logicalClusterLister.Cluster(clusterName).Get(corev1alpha1.LogicalClusterName)
		if err != nil {
			return nil, err
		}
		shardName := logicalCluster.Annotations[corev1alpha1.LogicalClusterShardAnnotationKey]
		if shardName == "" {
			return nil, fmt.Errorf("logical cluster %s is not annotated with its shard yet", clusterName)
		}

		shard, err := indexers.ShardByName(shardLister, shardName)
		if err != nil {
			return nil, err
		}
		return url.Parse(shard.Spec.BaseURL)
	}
}

type routingRoundTripper struct {
	delegate          http.RoundTripper
	baseURLForCluster BaseURLFunc
}

func (r *routingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clusterName, ok := clusterNameFromPath(req.URL.Path)
	if !ok {
		return nil, fmt.Errorf("cannot route %s to a shard: not a request to a logical cluster", req.URL.Path)
	}
	baseURL, err := r.baseURLForCluster(clusterName)
	if err != nil {
		return nil, fmt.Errorf("cannot route request to the shard of logical cluster %s: %w", clusterName, err)
	}

	// a RoundTripper must not modify the request
	req = req.Clone(req.Context())
	req.URL.Scheme = baseURL.Scheme
	req.URL.Host = baseURL.Host
	req.URL.Path = strings.TrimSuffix(baseURL.Path, "/") + req.URL.Path
	req.Host = ""

	return r.delegate.RoundTrip(req)
}

func (r *routingRoundTripper) WrappedRoundTripper() http.RoundTripper {
	return r.delegate
}

// clusterNameFromPath returns the logical cluster of a request to /clusters/<name>/...
func clusterNameFromPath(path string) (logicalcluster.Name, bool) {
	rest, ok := strings.CutPrefix(path, "/clusters/")
	if !ok {
		return "", false
	}
	name, _, _ := strings.Cut(rest, "/")
	if name == "" || name == "*" {
		return "", false
	}
	return logicalcluster.Name(name), true
}
