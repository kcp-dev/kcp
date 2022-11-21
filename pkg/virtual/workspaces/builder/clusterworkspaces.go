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
	"time"

	kcprbacv1informers "github.com/kcp-dev/client-go/informers/rbac/v1"
	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apiserver/pkg/authentication/user"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	frameworkrbac "github.com/kcp-dev/kcp/pkg/virtual/framework/rbac"
	workspaceauth "github.com/kcp-dev/kcp/pkg/virtual/workspaces/authorization"
	"github.com/kcp-dev/kcp/pkg/virtual/workspaces/registry"
)

var _ registry.FilteredClusterWorkspaces = &authCacheClusterWorkspaces{}

// authCacheClusterWorkspaces implement registry.FilteredClusterWorkspaces using an
// authorization cache.
type authCacheClusterWorkspaces struct {
	// workspaceLister can enumerate workspace lists that enforce policy
	clusterWorkspaceLister workspaceauth.Lister
	// authCache is a cache of cluster workspaces and associated subjects for a given org.
	authCache *workspaceauth.AuthorizationCache
	// stopCh allows stopping the authCache for this org.
	stopCh chan struct{}
}

// CreateAndStartOrg creates an Org that contains all the required clients and caches to retrieve user workspaces inside an org
// As part of an Org, a WorkspaceAuthCache is created and ensured to be started.
func CreateAndStartOrg(
	org logicalcluster.Name,
	rbacInformers kcprbacv1informers.ClusterInterface,
	clusterWorkspaceInformer tenancyv1alpha1informers.ClusterWorkspaceClusterInformer,
	initialWatchers []workspaceauth.CacheWatcher,
	period time.Duration, jitterFactor float64, sliding bool,
) *authCacheClusterWorkspaces {
	authCache := workspaceauth.NewAuthorizationCache(
		workspaceauth.CacheTypeOrg,
		clusterWorkspaceInformer.Lister(),
		clusterWorkspaceInformer.Informer(),
		workspaceauth.NewReviewer(frameworkrbac.NewSubjectLocator(org, rbacInformers)),
		*workspaceauth.NewAttributesBuilder().
			Verb("get").
			Resource(tenancyv1alpha1.SchemeGroupVersion.WithResource("workspaces")).
			AttributesRecord,
		org,
		rbacInformers,
	)

	cws := &authCacheClusterWorkspaces{
		clusterWorkspaceLister: authCache,
		stopCh:                 make(chan struct{}),
		authCache:              authCache,
	}

	for _, watcher := range initialWatchers {
		authCache.AddWatcher(watcher)
	}

	cws.authCache.Run(period, jitterFactor, sliding, cws.stopCh)

	return cws
}

func (o *authCacheClusterWorkspaces) List(user user.Info, labelSelector labels.Selector, fieldSelector fields.Selector) (*tenancyv1alpha1.ClusterWorkspaceList, error) {
	return o.clusterWorkspaceLister.List(user, labelSelector, fieldSelector)
}

func (o *authCacheClusterWorkspaces) RemoveWatcher(watcher workspaceauth.CacheWatcher) {
	o.authCache.RemoveWatcher(watcher)
}

func (o *authCacheClusterWorkspaces) AddWatcher(watcher workspaceauth.CacheWatcher) {
	o.authCache.AddWatcher(watcher)
}

func (o *authCacheClusterWorkspaces) Ready() bool {
	return o.authCache.ReadyForAccess()
}

func (o *authCacheClusterWorkspaces) Stop() {
	o.stopCh <- struct{}{}
}
