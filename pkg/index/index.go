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
	"net/url"
	"strings"
	"sync"

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

	// ErrorCode is the HTTP error code to return for the request.
	// If this is set, the URL and Shard fields are ignored.
	ErrorCode int
}

// PathRewriter can rewrite a logical cluster path before the actual mapping through
// the index data.
type PathRewriter func(segments []string) []string

func New(rewriters []PathRewriter) *State {
	return &State{
		rewriters: rewriters,

		clusterShards:                    map[logicalcluster.Name]string{},
		shardClusterWorkspaceNameCluster: map[string]map[logicalcluster.Name]map[string]logicalcluster.Name{},
		shardClusterWorkspaceName:        map[string]map[logicalcluster.Name]string{},
		shardClusterParentCluster:        map[string]map[logicalcluster.Name]logicalcluster.Name{},
		shardBaseURLs:                    map[string]string{},
		// Experimental feature: allow mounts to be used with Workspaces
		// structure: (shard, logical cluster, workspace name) -> string serialized mount objects
		// This should be simplified once we promote this to workspace structure.
		shardClusterWorkspaceMountAnnotation: map[string]map[logicalcluster.Name]map[string]string{},

		// shardClusterWorkspaceNameErrorCode is a map of shar,logical cluster, workspace to error code when we want to return an error code
		// instead of a URL.
		shardClusterWorkspaceNameErrorCode: map[string]map[logicalcluster.Name]map[string]int{},
	}
}

// State watches Shards on the root shard, and then starts informers
// for every Shard, watching the Workspaces on them. It then
// updates the workspace index, which maps logical clusters to shard URLs.
type State struct {
	rewriters []PathRewriter

	lock                             sync.RWMutex
	clusterShards                    map[logicalcluster.Name]string                                    // logical cluster -> shard name
	shardClusterWorkspaceNameCluster map[string]map[logicalcluster.Name]map[string]logicalcluster.Name // (shard name, logical cluster, workspace name) -> logical cluster
	shardClusterWorkspaceName        map[string]map[logicalcluster.Name]string                         // (shard name, logical cluster) -> workspace name
	shardClusterParentCluster        map[string]map[logicalcluster.Name]logicalcluster.Name            // (shard name, logical cluster) -> parent logical cluster
	shardBaseURLs                    map[string]string                                                 // shard name -> base URL
	// Experimental feature: allow mounts to be used with Workspaces
	shardClusterWorkspaceMountAnnotation map[string]map[logicalcluster.Name]map[string]string // (shard name, logical cluster, workspace name) -> mount object string

	shardClusterWorkspaceNameErrorCode map[string]map[logicalcluster.Name]map[string]int // (shard name, logical cluster, workspace name) -> error code
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
			c.shardClusterWorkspaceName[shard] = map[logicalcluster.Name]string{}
			c.shardClusterParentCluster[shard] = map[logicalcluster.Name]logicalcluster.Name{}
		}
		if c.shardClusterWorkspaceNameCluster[shard][clusterName] == nil {
			c.shardClusterWorkspaceNameCluster[shard][clusterName] = map[string]logicalcluster.Name{}
		}
		c.shardClusterWorkspaceNameCluster[shard][clusterName][ws.Name] = logicalcluster.Name(ws.Spec.Cluster)
		c.shardClusterWorkspaceName[shard][logicalcluster.Name(ws.Spec.Cluster)] = ws.Name
		c.shardClusterParentCluster[shard][logicalcluster.Name(ws.Spec.Cluster)] = clusterName
	}

	if mountObjString := c.shardClusterWorkspaceMountAnnotation[shard][clusterName][ws.Name]; mountObjString != ws.Annotations[tenancyv1alpha1.ExperimentalWorkspaceMountAnnotationKey] {
		if c.shardClusterWorkspaceMountAnnotation[shard] == nil {
			c.shardClusterWorkspaceMountAnnotation[shard] = map[logicalcluster.Name]map[string]string{}
		}
		if c.shardClusterWorkspaceMountAnnotation[shard][clusterName] == nil {
			c.shardClusterWorkspaceMountAnnotation[shard][clusterName] = map[string]string{}
		}
		c.shardClusterWorkspaceMountAnnotation[shard][clusterName][ws.Name] = ws.Annotations[tenancyv1alpha1.ExperimentalWorkspaceMountAnnotationKey]
	}
}

func (c *State) DeleteWorkspace(shard string, ws *tenancyv1alpha1.Workspace) {
	clusterName := logicalcluster.From(ws)

	c.lock.RLock()
	_, foundCluster := c.shardClusterWorkspaceNameCluster[shard][clusterName][ws.Name]
	_, foundMount := c.shardClusterWorkspaceMountAnnotation[shard][clusterName][ws.Name]
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

		delete(c.shardClusterWorkspaceName[shard], logicalcluster.Name(ws.Spec.Cluster))
		if len(c.shardClusterWorkspaceName[shard]) == 0 {
			delete(c.shardClusterWorkspaceName, shard)
		}

		delete(c.shardClusterParentCluster[shard], logicalcluster.Name(ws.Spec.Cluster))
		if len(c.shardClusterParentCluster[shard]) == 0 {
			delete(c.shardClusterParentCluster, shard)
		}
	}

	if _, foundMount = c.shardClusterWorkspaceMountAnnotation[shard][clusterName][ws.Name]; foundMount {
		delete(c.shardClusterWorkspaceMountAnnotation[shard][clusterName], ws.Name)
		if len(c.shardClusterWorkspaceMountAnnotation[shard][clusterName]) == 0 {
			delete(c.shardClusterWorkspaceMountAnnotation[shard], clusterName)
		}
	}
	delete(c.shardClusterWorkspaceNameErrorCode[shard][clusterName], ws.Name)
	if len(c.shardClusterWorkspaceNameErrorCode[shard][clusterName]) == 0 {
		delete(c.shardClusterWorkspaceNameErrorCode[shard], clusterName)
	}
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

	for lc, gotShardName := range c.clusterShards {
		if shardName == gotShardName {
			delete(c.clusterShards, lc)
		}
	}
	delete(c.shardClusterWorkspaceNameCluster, shardName)
	delete(c.shardBaseURLs, shardName)
	delete(c.shardClusterWorkspaceName, shardName)
	delete(c.shardClusterParentCluster, shardName)
	delete(c.shardClusterWorkspaceNameErrorCode, shardName)
}

func (c *State) Lookup(path logicalcluster.Path) (Result, bool) {
	segments := strings.Split(path.String(), ":")

	for _, rewriter := range c.rewriters {
		segments = rewriter(segments)
	}

	c.lock.RLock()
	defer c.lock.RUnlock()

	var shard string
	var cluster logicalcluster.Name
	var errorCode int

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

		// check mounts, if found return url and true
		val, foundMount := c.shardClusterWorkspaceMountAnnotation[shard][cluster][s] // experimental feature
		if foundMount {
			mount, err := tenancyv1alpha1.ParseTenancyMountAnnotation(val)
			if !(err != nil || mount == nil || mount.MountStatus.URL == "") {
				u, err := url.Parse(mount.MountStatus.URL)
				if err != nil {
					// default to workspace itself.
				} else {
					return Result{URL: u.String()}, true
				}
			}
		}

		if ec, found := c.shardClusterWorkspaceNameErrorCode[shard][cluster][s]; found {
			errorCode = ec
		}
		var found bool
		cluster, found = c.shardClusterWorkspaceNameCluster[shard][cluster][s]
		if !found {
			return Result{}, false
		}
		shard, found = c.clusterShards[cluster]
		if !found {
			return Result{}, false
		}
	}
	return Result{Shard: shard, Cluster: cluster, ErrorCode: errorCode}, true
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
		URL: strings.TrimSuffix(baseURL, "/") + result.Cluster.Path().RequestPath(),
	}, true
}
