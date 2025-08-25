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

package index

import (
	"maps"
	"net/url"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/api/equality"

	"github.com/kcp-dev/logicalcluster/v3"

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
)

// Index implements a mapping from logical cluster to (shard) URL.
type Index interface {
	Lookup(path logicalcluster.Path) (Result, bool)
	LookupURL(logicalCluster logicalcluster.Path) (Result, bool)
}

// Result is the result of a lookup.
type Result struct {
	URL   string
	Shard string
	// Cluster canonical path
	Cluster logicalcluster.Name
	// Type is not set for mounted workspaces. For all others this value is the
	// fully-qualified name of the type, e.g. "root:universal".
	Type logicalcluster.Path

	// ErrorCode is the HTTP error code to return for the request.
	// If this is set, the URL and Shard fields are ignored.
	ErrorCode int
}

// PathRewriter can rewrite a logical cluster path before the actual mapping through
// the index data.
type PathRewriter func(segments []string) []string

// State is a runtime graph of shards, workspaces, logicalclusters and mounts.
// It usually gets fed by a controller that watches these resources on each
// shard. The state then organizes the graph and offers a workspace path lookup
// that resolves a workspace path to its physical location on a shard and logical
// cluster.
type State struct {
	rewriters []PathRewriter

	lock                             sync.RWMutex
	clusterShards                    map[logicalcluster.Name]string                                    // logical cluster -> shard name
	shardClusterWorkspaceNameCluster map[string]map[logicalcluster.Name]map[string]logicalcluster.Name // (shard name, logical cluster, workspace name) -> logical cluster
	shardClusterWorkspaceName        map[string]map[logicalcluster.Name]string                         // (shard name, logical cluster) -> workspace name
	shardClusterWorkspaceType        map[string]map[logicalcluster.Name]logicalcluster.Path            // (shard name, logical cluster) -> workspace type
	shardClusterParentCluster        map[string]map[logicalcluster.Name]logicalcluster.Name            // (shard name, logical cluster) -> parent logical cluster
	shardBaseURLs                    map[string]string                                                 // shard name -> base URL
	// Experimental feature: allow mounts to be used with Workspaces
	shardClusterWorkspaceMount map[string]map[logicalcluster.Name]map[string]tenancyv1alpha1.WorkspaceSpec // (shard name, logical cluster, workspace name) -> WorkspaceSpec

	shardClusterWorkspaceNameErrorCode map[string]map[logicalcluster.Name]map[string]int // (shard name, logical cluster, workspace name) -> error code
}

func New(rewriters []PathRewriter) *State {
	return &State{
		rewriters: rewriters,

		clusterShards:                    map[logicalcluster.Name]string{},
		shardClusterWorkspaceNameCluster: map[string]map[logicalcluster.Name]map[string]logicalcluster.Name{},
		shardClusterWorkspaceName:        map[string]map[logicalcluster.Name]string{},
		shardClusterWorkspaceType:        map[string]map[logicalcluster.Name]logicalcluster.Path{},
		shardClusterParentCluster:        map[string]map[logicalcluster.Name]logicalcluster.Name{},
		shardBaseURLs:                    map[string]string{},
		// Experimental feature: allow mounts to be used with Workspaces
		// structure: (shard, logical cluster, workspace name) -> string serialized mount objects
		// This should be simplified once we promote this to workspace structure.
		shardClusterWorkspaceMount: map[string]map[logicalcluster.Name]map[string]tenancyv1alpha1.WorkspaceSpec{},

		// shardClusterWorkspaceNameErrorCode is a map of shar,logical cluster, workspace to error code when we want to return an error code
		// instead of a URL.
		shardClusterWorkspaceNameErrorCode: map[string]map[logicalcluster.Name]map[string]int{},
	}
}

func (c *State) UpsertWorkspace(shard string, ws *tenancyv1alpha1.Workspace) {
	if ws.Status.Phase == corev1alpha1.LogicalClusterPhaseScheduling {
		return
	}

	clusterName := logicalcluster.From(ws)

	c.lock.Lock()
	defer c.lock.Unlock()

	// If the workspace is unavailable, we set custom error code for it. And add it to the index as normal.
	// TODO(mjudeikis): Once we have one more case - move to a separate function.
	if ws.Status.Phase == corev1alpha1.LogicalClusterPhaseUnavailable {
		if c.shardClusterWorkspaceNameErrorCode[shard] == nil {
			c.shardClusterWorkspaceNameErrorCode[shard] = map[logicalcluster.Name]map[string]int{}
		}
		if c.shardClusterWorkspaceNameErrorCode[shard][clusterName] == nil {
			c.shardClusterWorkspaceNameErrorCode[shard][clusterName] = map[string]int{}
		}
		// Unavailable workspaces should return 503
		c.shardClusterWorkspaceNameErrorCode[shard][clusterName][ws.Name] = 503
	} else {
		delete(c.shardClusterWorkspaceNameErrorCode[shard][clusterName], ws.Name)
		if len(c.shardClusterWorkspaceNameErrorCode[shard][clusterName]) == 0 {
			delete(c.shardClusterWorkspaceNameErrorCode[shard], clusterName)
		}
	}

	if cluster := c.shardClusterWorkspaceNameCluster[shard][clusterName][ws.Name]; cluster.String() != ws.Spec.Cluster {
		if c.shardClusterWorkspaceNameCluster[shard] == nil {
			c.shardClusterWorkspaceNameCluster[shard] = map[logicalcluster.Name]map[string]logicalcluster.Name{}
		}
		if c.shardClusterWorkspaceName[shard] == nil {
			c.shardClusterWorkspaceName[shard] = map[logicalcluster.Name]string{}
		}
		if c.shardClusterParentCluster[shard] == nil {
			c.shardClusterParentCluster[shard] = map[logicalcluster.Name]logicalcluster.Name{}
		}
		if c.shardClusterWorkspaceNameCluster[shard][clusterName] == nil {
			c.shardClusterWorkspaceNameCluster[shard][clusterName] = map[string]logicalcluster.Name{}
		}

		c.shardClusterWorkspaceNameCluster[shard][clusterName][ws.Name] = logicalcluster.Name(ws.Spec.Cluster)
		c.shardClusterWorkspaceName[shard][logicalcluster.Name(ws.Spec.Cluster)] = ws.Name
		c.shardClusterParentCluster[shard][logicalcluster.Name(ws.Spec.Cluster)] = clusterName
	}

	if ws.Spec.Mount != nil {
		if wsSpec := c.shardClusterWorkspaceMount[shard][clusterName][ws.Name]; !equality.Semantic.DeepEqual(wsSpec, ws.Spec) {
			if c.shardClusterWorkspaceMount[shard] == nil {
				c.shardClusterWorkspaceMount[shard] = map[logicalcluster.Name]map[string]tenancyv1alpha1.WorkspaceSpec{}
			}
			if c.shardClusterWorkspaceMount[shard][clusterName] == nil {
				c.shardClusterWorkspaceMount[shard][clusterName] = map[string]tenancyv1alpha1.WorkspaceSpec{}
			}
			c.shardClusterWorkspaceMount[shard][clusterName][ws.Name] = ws.Spec
		}
	}

	clustersOnShard.WithLabelValues(shard).Set(float64(len(c.shardClusterWorkspaceName[shard])))
}

func (c *State) DeleteWorkspace(shard string, ws *tenancyv1alpha1.Workspace) {
	clusterName := logicalcluster.From(ws)

	c.lock.RLock()
	_, foundCluster := c.shardClusterWorkspaceNameCluster[shard][clusterName][ws.Name]
	_, foundMount := c.shardClusterWorkspaceMount[shard][clusterName][ws.Name]
	c.lock.RUnlock()

	if !foundCluster && !foundMount {
		return
	}

	c.lock.Lock()
	defer c.lock.Unlock()

	if _, foundCluster = c.shardClusterWorkspaceNameCluster[shard][clusterName][ws.Name]; foundCluster {
		delete(c.shardClusterWorkspaceNameCluster[shard][clusterName], ws.Name)
		if len(c.shardClusterWorkspaceNameCluster[shard][clusterName]) == 0 {
			delete(c.shardClusterWorkspaceNameCluster[shard], clusterName)
		}
		if len(c.shardClusterWorkspaceNameCluster[shard]) == 0 {
			delete(c.shardClusterWorkspaceNameCluster, shard)
		}

		cluster := logicalcluster.Name(ws.Spec.Cluster)

		delete(c.shardClusterWorkspaceName[shard], cluster)
		if len(c.shardClusterWorkspaceName[shard]) == 0 {
			delete(c.shardClusterWorkspaceName, shard)
		}

		delete(c.shardClusterParentCluster[shard], cluster)
		if len(c.shardClusterParentCluster[shard]) == 0 {
			delete(c.shardClusterParentCluster, shard)
		}
	}

	if _, foundMount = c.shardClusterWorkspaceMount[shard][clusterName][ws.Name]; foundMount {
		delete(c.shardClusterWorkspaceMount[shard][clusterName], ws.Name)
		if len(c.shardClusterWorkspaceMount[shard][clusterName]) == 0 {
			delete(c.shardClusterWorkspaceMount[shard], clusterName)
			if len(c.shardClusterWorkspaceMount[shard]) == 0 {
				delete(c.shardClusterWorkspaceName, shard)
			}
		}
	}

	delete(c.shardClusterWorkspaceNameErrorCode[shard][clusterName], ws.Name)
	if len(c.shardClusterWorkspaceNameErrorCode[shard][clusterName]) == 0 {
		delete(c.shardClusterWorkspaceNameErrorCode[shard], clusterName)
		if len(c.shardClusterWorkspaceNameErrorCode[shard]) == 0 {
			delete(c.shardClusterWorkspaceNameErrorCode, shard)
		}
	}

	clustersOnShard.WithLabelValues(shard).Set(float64(len(c.shardClusterWorkspaceName[shard])))
}

func (c *State) UpsertLogicalCluster(shard string, logicalCluster *corev1alpha1.LogicalCluster) {
	clusterName := logicalcluster.From(logicalCluster)

	c.lock.RLock()
	got := c.clusterShards[clusterName]
	c.lock.RUnlock()

	if got != shard {
		c.lock.Lock()
		defer c.lock.Unlock()

		c.clusterShards[clusterName] = shard

		// LogicalClusters are annotated with "path:name" of their workspace's type.
		typeIdent := logicalcluster.NewPath(logicalCluster.Annotations[tenancyv1alpha1.LogicalClusterTypeAnnotationKey])

		if c.shardClusterWorkspaceType[shard] == nil {
			c.shardClusterWorkspaceType[shard] = map[logicalcluster.Name]logicalcluster.Path{}
		}
		c.shardClusterWorkspaceType[shard][clusterName] = typeIdent
	}
}

func (c *State) DeleteLogicalCluster(shard string, logicalCluster *corev1alpha1.LogicalCluster) {
	clusterName := logicalcluster.From(logicalCluster)

	c.lock.RLock()
	got := c.clusterShards[clusterName]
	c.lock.RUnlock()

	if got == shard {
		c.lock.Lock()
		defer c.lock.Unlock()
		if got := c.clusterShards[clusterName]; got == shard {
			delete(c.clusterShards, clusterName)
		}

		delete(c.shardClusterWorkspaceType[shard], clusterName)
		if len(c.shardClusterWorkspaceType[shard]) == 0 {
			delete(c.shardClusterWorkspaceType, shard)
		}
	}
}

func (c *State) UpsertShard(shardName, baseURL string) {
	c.lock.RLock()
	got := c.shardBaseURLs[shardName]
	c.lock.RUnlock()

	if got != baseURL {
		c.lock.Lock()
		defer c.lock.Unlock()
		c.shardBaseURLs[shardName] = baseURL
	}
}

func (c *State) DeleteShard(shardName string) {
	c.lock.Lock()
	defer c.lock.Unlock()

	maps.DeleteFunc(c.clusterShards, func(_ logicalcluster.Name, shard string) bool {
		return shardName == shard
	})

	delete(c.shardClusterWorkspaceNameCluster, shardName)
	delete(c.shardBaseURLs, shardName)
	delete(c.shardClusterWorkspaceName, shardName)
	delete(c.shardClusterWorkspaceType, shardName)
	delete(c.shardClusterParentCluster, shardName)
	delete(c.shardClusterWorkspaceNameErrorCode, shardName)

	clustersOnShard.DeleteLabelValues(shardName)
}

func (c *State) Lookup(path logicalcluster.Path) (Result, bool) {
	segments := strings.Split(path.String(), ":")

	for _, rewriter := range c.rewriters {
		segments = rewriter(segments)
	}

	c.lock.RLock()
	defer c.lock.RUnlock()

	var (
		shard     string
		cluster   logicalcluster.Name
		wsType    logicalcluster.Path
		errorCode int
	)

	// walk through index graph to find the final logical cluster and shard
	for i, s := range segments {
		if i == 0 {
			var found bool
			shard, found = c.clusterShards[logicalcluster.Name(s)]
			if !found {
				return Result{}, false
			}
			cluster = logicalcluster.Name(s)
			continue
		}

		if ec, found := c.shardClusterWorkspaceNameErrorCode[shard][cluster][s]; found {
			errorCode = ec
		}
		var found bool
		originalCluster := cluster
		cluster, found = c.shardClusterWorkspaceNameCluster[shard][cluster][s]
		if !found {
			// We not gonna find the cluster if we using mounts and spec.cluster was never set.
			// Lets check if we have a mount for this workspace, and if we do, we can return the URL from the mount.
			// Else we get back to default behavior.
			wsSpec, foundMount := c.shardClusterWorkspaceMount[shard][originalCluster][s] // experimental feature
			if foundMount {
				if wsSpec.Mount != nil && wsSpec.URL != "" {
					u, err := url.Parse(wsSpec.URL)
					if err == nil {
						return Result{URL: u.String(), ErrorCode: errorCode}, true
					}
				}
			}
			return Result{}, false
		}

		shard, found = c.clusterShards[cluster]
		if !found {
			return Result{}, false
		}
	}

	wsType = c.shardClusterWorkspaceType[shard][cluster]

	return Result{
		Shard:     shard,
		Cluster:   cluster,
		Type:      wsType,
		ErrorCode: errorCode,
	}, true
}

func (c *State) LookupURL(path logicalcluster.Path) (Result, bool) {
	result, found := c.Lookup(path)
	if !found {
		return Result{}, false
	}
	if result.ErrorCode != 0 {
		return result, true
	}

	if result.URL != "" && result.Shard == "" && result.Cluster == "" {
		return result, true
	}

	baseURL, found := c.shardBaseURLs[result.Shard]
	if !found {
		return Result{}, false
	}

	return Result{
		Shard:   result.Shard,
		Cluster: result.Cluster,
		Type:    result.Type,
		URL:     strings.TrimSuffix(baseURL, "/") + result.Cluster.Path().RequestPath(),
	}, true
}
