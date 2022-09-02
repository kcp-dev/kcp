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

package builder

import (
	"sync"

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	tenancylisters "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/virtual/workspaces/authorization"
	"github.com/kcp-dev/kcp/pkg/virtual/workspaces/registry"
)

type Stoppable interface {
	Stop()
}

// orgListener *-watches ClusterWorkspaces and starts virtualworkspacesregistry.Org for the
// parents for those. This means that workspaces without any ClusterWorkspace (like universal
// workspaces) will not be started.
type orgListener struct {
	lister   tenancylisters.ClusterWorkspaceLister
	informer cache.SharedIndexInformer

	newClusterWorkspaces func(orgClusterName logicalcluster.Name, initialWatchers []authorization.CacheWatcher) registry.FilteredClusterWorkspaces

	lock sync.RWMutex

	clusterWorkspacesPerCluster map[logicalcluster.Name]*preCreationClusterWorkspaces
}

func NewOrgListener(informer tenancyinformers.ClusterWorkspaceInformer, newClusterWorkspaces func(orgClusterName logicalcluster.Name, initialWatchers []authorization.CacheWatcher) registry.FilteredClusterWorkspaces) *orgListener {
	l := &orgListener{
		lister:   informer.Lister(),
		informer: informer.Informer(),

		newClusterWorkspaces:        newClusterWorkspaces,
		clusterWorkspacesPerCluster: map[logicalcluster.Name]*preCreationClusterWorkspaces{},
	}

	// nolint: errcheck
	informer.Informer().AddIndexers(cache.Indexers{
		"parent": indexByLogicalCluster,
	})

	informer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc:    l.addClusterWorkspace,
			DeleteFunc: l.deleteClusterWorkspace,
		},
	)

	return l
}

func (l *orgListener) Stop() {
	l.lock.RLock()
	defer l.lock.RUnlock()

	for _, o := range l.clusterWorkspacesPerCluster {
		o.Stop()
	}
}

// FilteredClusterWorkspaces returns the cluster workspace provider or nil if it is started (does not mean it does
// not exist, we just don't know here).
// Note: because the defining ClusterWorkspace of the parent can be on a different shard, we cannot know here.
func (l *orgListener) FilteredClusterWorkspaces(orgName logicalcluster.Name) registry.FilteredClusterWorkspaces {
	// fast path
	l.lock.RLock()
	cws, found := l.clusterWorkspacesPerCluster[orgName]
	l.lock.RUnlock()
	if found {
		return cws
	}

	// slow path
	l.lock.Lock()
	defer l.lock.Unlock()
	if _, found := l.clusterWorkspacesPerCluster[orgName]; found {
		return cws
	}

	l.clusterWorkspacesPerCluster[orgName] = &preCreationClusterWorkspaces{}
	return l.clusterWorkspacesPerCluster[orgName]
}

func (l *orgListener) addClusterWorkspace(obj interface{}) {
	cw, ok := obj.(*tenancyv1alpha1.ClusterWorkspace)
	if !ok {
		klog.Errorf("expected ClusterWorkspace but handler received %#v", obj)
		return
	}

	// fast path
	parent := logicalcluster.From(cw)
	l.lock.RLock()
	cws, found := l.clusterWorkspacesPerCluster[parent]
	if found {
		cws.lock.RLock()
		if cws.delegate != nil {
			cws.lock.RUnlock()
			l.lock.RUnlock()
			return
		}
		cws.lock.RUnlock()
	}
	l.lock.RUnlock()

	// slow path
	l.lock.Lock()
	defer l.lock.Unlock()
	cws, found = l.clusterWorkspacesPerCluster[parent]
	var existingWatchers []authorization.CacheWatcher
	if found {
		cws.lock.RLock()
		if cws.delegate != nil {
			cws.lock.RUnlock()
			return
		}
		cws.lock.RUnlock()

		// there is no auth cache running yet. Start one.
		cws.lock.Lock()
		defer cws.lock.Unlock()
		existingWatchers = cws.watchers
		cws.watchers = nil
	} else {
		cws = &preCreationClusterWorkspaces{}
		l.clusterWorkspacesPerCluster[parent] = cws
	}

	klog.Infof("First ClusterWorkspace for %s, starting authorization cache", parent)
	l.clusterWorkspacesPerCluster[parent].delegate = l.newClusterWorkspaces(parent, existingWatchers)
}

func (l *orgListener) deleteClusterWorkspace(obj interface{}) {
	cw, ok := obj.(*tenancyv1alpha1.ClusterWorkspace)
	if !ok {
		klog.Errorf("Expected ClusterWorkspace but handler received %#v", obj)
		return
	}

	// fast path
	parent := logicalcluster.From(cw)
	l.lock.RLock()
	_, found := l.clusterWorkspacesPerCluster[parent]
	l.lock.RUnlock()
	if !found {
		return
	}

	// any other ClusterWorkspace in this logical cluster?
	others, err := l.informer.GetIndexer().ByIndex("parent", parent.String())
	if err != nil {
		klog.Errorf("Failed to get ClusterWorkspace parent index %v: %v", parent, err)
		return
	}
	if len(others) > 0 {
		return
	}

	// slow path
	l.lock.Lock()
	defer l.lock.Unlock()
	cws, found := l.clusterWorkspacesPerCluster[parent]
	if !found {
		return
	}

	klog.Infof("Last ClusterWorkspace for %s is gone", parent)
	// Note: this will stop watches on last ClusterWorkspace removal. Not perfect, but ok.
	cws.Stop()
	delete(l.clusterWorkspacesPerCluster, parent)
}

// preCreationClusterWorkspaces is a proxy object that collects watchers before the actual auth cache is started.
// On auth cache start, the collected watchers are added to the auth cache.
type preCreationClusterWorkspaces struct {
	lock     sync.RWMutex
	watchers []authorization.CacheWatcher
	delegate registry.FilteredClusterWorkspaces
}

func (cws *preCreationClusterWorkspaces) List(user user.Info, labelSelector labels.Selector, fieldSelector fields.Selector) (*tenancyv1alpha1.ClusterWorkspaceList, error) {
	cws.lock.RLock()
	defer cws.lock.RUnlock()
	if cws.delegate != nil {
		return cws.delegate.List(user, labelSelector, fieldSelector)
	}
	return &tenancyv1alpha1.ClusterWorkspaceList{}, nil
}

func (cws *preCreationClusterWorkspaces) RemoveWatcher(watcher authorization.CacheWatcher) {
	// fast path
	cws.lock.RLock()
	if cws.delegate != nil {
		cws.delegate.RemoveWatcher(watcher)
		cws.lock.RUnlock()
		return
	}
	cws.lock.RUnlock()

	// slow path
	cws.lock.Lock()
	defer cws.lock.Unlock()
	if cws.delegate != nil {
		cws.delegate.RemoveWatcher(watcher)
		return
	}

	for i, w := range cws.watchers {
		if w == watcher {
			cws.watchers = append(cws.watchers[:i], cws.watchers[i+1:]...)
			return
		}
	}
}

func (cws *preCreationClusterWorkspaces) AddWatcher(watcher authorization.CacheWatcher) {
	// fast path
	cws.lock.RLock()
	if cws.delegate != nil {
		cws.delegate.AddWatcher(watcher)
		cws.lock.RUnlock()
		return
	}
	cws.lock.RUnlock()

	// slow path
	cws.lock.Lock()
	defer cws.lock.Unlock()
	if cws.delegate != nil {
		cws.delegate.AddWatcher(watcher)
		return
	}
	cws.watchers = append(cws.watchers, watcher)
}

func (cws *preCreationClusterWorkspaces) Stop() {
	cws.lock.Lock()
	defer cws.lock.Unlock()
	for _, w := range cws.watchers {
		if w, ok := w.(Stoppable); ok {
			w.Stop()
		}
	}
}
