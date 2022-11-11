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
	"strings"
	"sync"

	"github.com/kcp-dev/logicalcluster/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

// Index implements a mapping from logical cluster to (shard) URL.
type Index interface {
	LookupShardAndCluster(path logicalcluster.Name) (string, logicalcluster.Name, bool)
	LookupURL(logicalCluster logicalcluster.Name) (string, bool)
}

// PathRewriter can rewrite a logical cluster path before the actual mapping through
// the index data.
type PathRewriter func(segments []string) []string

func New(rewriters []PathRewriter) *State {
	return &State{
		rewriters: rewriters,

		logicalClusterShards: map[logicalcluster.Name]string{},
		shardWorkspaces:      map[string]map[logicalcluster.Name]map[string]logicalcluster.Name{},
		shardBaseURLs:        map[string]string{},
	}
}

// State watches ClusterWorkspaceShards on the root shard, and then starts informers
// for every ClusterWorkspaceShard, watching the ClusterWorkspaces on them. It then
// updates the workspace index, which maps logical clusters to shard URLs.
type State struct {
	rewriters []PathRewriter

	lock                 sync.RWMutex
	logicalClusterShards map[logicalcluster.Name]string                                    // logical cluster -> shard name
	shardWorkspaces      map[string]map[logicalcluster.Name]map[string]logicalcluster.Name // shard name -> logical cluster -> workspace -> logical cluster
	shardBaseURLs        map[string]string                                                 // shard name -> base URL
}

func (c *State) UpsertClusterWorkspace(shard string, ws *tenancyv1alpha1.ClusterWorkspace) {
	if ws.Status.Phase == tenancyv1alpha1.WorkspacePhaseScheduling {
		return
	}
	clusterName := logicalcluster.From(ws)

	c.lock.RLock()
	got := c.shardWorkspaces[shard][clusterName][ws.Name]
	c.lock.RUnlock()

	if got.String() == ws.Status.Cluster {
		return
	}

	c.lock.Lock()
	defer c.lock.Unlock()

	if got := c.shardWorkspaces[shard][clusterName][ws.Name]; got.String() != ws.Status.Cluster {
		if c.shardWorkspaces[shard] == nil {
			c.shardWorkspaces[shard] = map[logicalcluster.Name]map[string]logicalcluster.Name{}
		}
		if c.shardWorkspaces[shard][clusterName] == nil {
			c.shardWorkspaces[shard][clusterName] = map[string]logicalcluster.Name{}
		}
		c.shardWorkspaces[shard][clusterName][ws.Name] = logicalcluster.New(ws.Status.Cluster)
	}
}

func (c *State) DeleteClusterWorkspace(shard string, ws *tenancyv1alpha1.ClusterWorkspace) {
	if ws.Status.Phase == tenancyv1alpha1.WorkspacePhaseScheduling {
		return
	}
	clusterName := logicalcluster.From(ws)

	c.lock.RLock()
	_, found := c.shardWorkspaces[shard][clusterName][ws.Name]
	c.lock.RUnlock()

	if !found {
		return
	}

	c.lock.Lock()
	defer c.lock.Unlock()

	if _, found = c.shardWorkspaces[shard][clusterName][ws.Name]; !found {
		return
	}

	delete(c.shardWorkspaces[shard][clusterName], ws.Name)
	if len(c.shardWorkspaces[shard][clusterName]) == 0 {
		delete(c.shardWorkspaces[shard], clusterName)
	}
	if len(c.shardWorkspaces[shard]) == 0 {
		delete(c.shardWorkspaces, shard)
	}
}

func (c *State) UpsertThisWorkspace(shard string, this *tenancyv1alpha1.ThisWorkspace) {
	clusterName := logicalcluster.From(this)

	c.lock.RLock()
	got := c.logicalClusterShards[clusterName]
	c.lock.RUnlock()

	if got != shard {
		c.lock.Lock()
		defer c.lock.Unlock()
		c.logicalClusterShards[clusterName] = shard
	}
}

func (c *State) DeleteThisWorkspace(shard string, this *tenancyv1alpha1.ThisWorkspace) {
	clusterName := logicalcluster.From(this)

	c.lock.RLock()
	got := c.logicalClusterShards[clusterName]
	c.lock.RUnlock()

	if got == shard {
		c.lock.Lock()
		defer c.lock.Unlock()
		if got := c.logicalClusterShards[clusterName]; got == shard {
			delete(c.logicalClusterShards, clusterName)
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

	for lc, gotShardName := range c.logicalClusterShards {
		if shardName == gotShardName {
			delete(c.logicalClusterShards, lc)
		}
	}
	delete(c.shardWorkspaces, shardName)
	delete(c.shardBaseURLs, shardName)
}

func (c *State) LookupShardAndCluster(path logicalcluster.Name) (string, logicalcluster.Name, bool) {
	segments := strings.Split(path.String(), ":")

	for _, rewriter := range c.rewriters {
		segments = rewriter(segments)
	}

	c.lock.RLock()
	defer c.lock.RUnlock()

	// walk through index graph to find the final logical cluster and shard
	var cluster logicalcluster.Name
	var shard string
	for i, s := range segments {
		if i == 0 {
			var found bool
			shard, found = c.logicalClusterShards[logicalcluster.New(s)]
			if !found {
				return "", logicalcluster.Name{}, false
			}
			cluster = logicalcluster.New(s)
			continue
		}

		var found bool
		cluster, found = c.shardWorkspaces[shard][cluster][s]
		if !found {
			return "", logicalcluster.Name{}, false
		}
		shard, found = c.logicalClusterShards[cluster]
		if !found {
			return "", logicalcluster.Name{}, false
		}
	}

	return shard, cluster, true
}

func (c *State) LookupURL(path logicalcluster.Name) (string, bool) {
	shard, cluster, found := c.LookupShardAndCluster(path)
	if !found {
		return "", false
	}

	baseURL, found := c.shardBaseURLs[shard]
	if !found {
		return "", false
	}

	return strings.TrimSuffix(baseURL, "/") + cluster.Path(), true
}
