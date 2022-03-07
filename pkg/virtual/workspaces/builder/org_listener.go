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
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	workspaceauth "github.com/kcp-dev/kcp/pkg/virtual/workspaces/auth"
	workspacecache "github.com/kcp-dev/kcp/pkg/virtual/workspaces/cache"
	virtualworkspacesregistry "github.com/kcp-dev/kcp/pkg/virtual/workspaces/registry"
)

// orgListener listens for changes in the root WorkspaceAuthCache,
// in order to manage the list of orgs
type orgListener struct {
	newOrg func(orgName string) virtualworkspacesregistry.Org

	clusterWorkspaceCache *workspacecache.ClusterWorkspaceCache

	knownWorkspacesMutex sync.Mutex
	// knownWorkspaces keeps the list of the workspaces that are currently known to exist,
	// so that when a notification comes from the workspaceAuthCache for a workspace
	// (through the GroupMembershipChanged method being called), we can conclude
	// whether the workspace has been deleted, added or modified.
	knownWorkspaces sets.String

	ready func() bool

	orgMutex sync.RWMutex
	orgs     map[string]virtualworkspacesregistry.Org
}

func NewOrgListener(clusterWorkspaceCache *workspacecache.ClusterWorkspaceCache, rootOrg virtualworkspacesregistry.Org, newOrg func(orgName string) virtualworkspacesregistry.Org) *orgListener {
	w := &orgListener{
		newOrg: newOrg,
		orgs: map[string]virtualworkspacesregistry.Org{
			helper.RootCluster: rootOrg,
		},

		clusterWorkspaceCache: clusterWorkspaceCache,

		knownWorkspaces: sets.NewString(),

		ready: func() bool { return false },
	}
	return w
}

func (l *orgListener) Initialize(authCache *workspaceauth.AuthorizationCache) {
	l.knownWorkspacesMutex.Lock()
	defer l.knownWorkspacesMutex.Unlock()

	l.orgMutex.Lock()
	defer l.orgMutex.Unlock()

	var mutex sync.Mutex
	mutex.Lock()
	defer mutex.Unlock()

	readys := []func() bool{}

	workspaces, _ := authCache.ListAllWorkspaces(labels.Everything())
	for _, workspace := range workspaces.Items {
		orgName := helper.RootCluster + ":" + workspace.Name
		org := l.newOrg(orgName)
		readys = append(readys, org.ReadyForAccess)
		l.knownWorkspaces.Insert(workspace.Name)
		l.orgs[orgName] = org
	}
	l.ready = func() bool {
		mutex.Lock()
		defer mutex.Unlock()

		for _, r := range readys {
			if !r() {
				return false
			}
		}
		return true
	}
	authCache.AddWatcher(l)
}

func (l *orgListener) Stop() {
	l.orgMutex.RLock()
	defer l.orgMutex.RUnlock()

	for _, o := range l.orgs {
		o.Stop()
	}
}

func (l *orgListener) GroupMembershipChanged(workspaceName string, users, groups sets.String) {
	l.knownWorkspacesMutex.Lock()
	defer l.knownWorkspacesMutex.Unlock()

	orgName := helper.RootCluster + ":" + workspaceName
	// All the workspace objects are accessible to the "system:masters" group
	// and we want to track changes to all workspaces here
	hasAccess := groups.Has(user.SystemPrivilegedGroup)
	known := l.knownWorkspaces.Has(workspaceName)
	switch {
	// this means that the workspace has been deleted
	case !hasAccess && known:
		l.knownWorkspaces.Delete(workspaceName)

		l.RemoveOrg(orgName)

	case hasAccess:
		_, err := l.clusterWorkspaceCache.GetWorkspace(helper.RootCluster, workspaceName)
		if err != nil {
			utilruntime.HandleError(err)
			return
		}

		// if we already have this in our list, then we're getting notified because the object changed
		if known := l.knownWorkspaces.Has(workspaceName); known {
			return
		}
		l.knownWorkspaces.Insert(workspaceName)
		if err := l.AddOrg(orgName); err != nil {
			klog.Errorf("Failed adding org: %s: %v", orgName, err)
		}
	}

}

func (l *orgListener) AddOrg(orgName string) error {
	l.orgMutex.Lock()
	defer l.orgMutex.Unlock()

	if _, exists := l.orgs[orgName]; !exists {
		org := l.newOrg(orgName)
		if err := wait.PollImmediate(100*time.Millisecond, 1*time.Minute, func() (done bool, err error) {
			return org.ReadyForAccess(), nil
		}); err != nil {
			org.Stop()
			return err
		}
		l.orgs[orgName] = org
	}
	return nil
}

func (l *orgListener) RemoveOrg(orgName string) {
	l.orgMutex.Lock()
	defer l.orgMutex.Unlock()

	if org, exists := l.orgs[orgName]; exists {
		delete(l.orgs, orgName)
		org.Stop()
	}
}

func (l *orgListener) Ready() bool {
	l.orgMutex.RLock()
	defer l.orgMutex.RUnlock()

	return l.ready()
}

func (l *orgListener) GetOrg(orgName string) (virtualworkspacesregistry.Org, error) {
	l.orgMutex.RLock()
	defer l.orgMutex.RUnlock()

	org, exists := l.orgs[orgName]
	if !exists {
		return virtualworkspacesregistry.Org{}, fmt.Errorf("Unknown organization: %s", orgName)
	}
	return org, nil
}
