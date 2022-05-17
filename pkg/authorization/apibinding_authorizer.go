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

package authorization

import (
	"context"

	"github.com/kcp-dev/logicalcluster"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	clientgoinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	klog "k8s.io/klog/v2"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	rbacwrapper "github.com/kcp-dev/kcp/pkg/virtual/framework/wrappers/rbac"
)

const (
	byWorkspaceIndex = "apiBindingAuthorizer-byWorkspace"
)

// NewAPIBindingAccessAuthorizer returns an authorizer that checks if the the request is for a
// bound resource or not. If the resource is bound we will check the user has RBAC access in the
// exported resources workspace. If it is not allowed we will return NoDecision, if allowed we
// will call the delegate authorizer.
func NewAPIBindingAccessAuthorizer(kubeInformers clientgoinformers.SharedInformerFactory, kcpInformers kcpinformers.SharedInformerFactory, delegate authorizer.Authorizer) authorizer.Authorizer {
	if _, found := kcpInformers.Apis().V1alpha1().APIBindings().Informer().GetIndexer().GetIndexers()[byWorkspaceIndex]; !found {
		err := kcpInformers.Apis().V1alpha1().APIBindings().Informer().AddIndexers(
			cache.Indexers{
				byWorkspaceIndex: func(obj interface{}) ([]string, error) {
					return []string{logicalcluster.From(obj.(metav1.Object)).String()}, nil
				},
			},
		)
		if err != nil {
			// nothing we can do here. But this should also never happen. We check for existence before.
			klog.Errorf("failed to add indexer for APIBindings: %v", err)
		}
	}

	return &apiBindingAccessAuthorizer{
		versionedInformers: kubeInformers,
		apiBindingIndexer:  kcpInformers.Apis().V1alpha1().APIBindings().Informer().GetIndexer(),
		delegate:           delegate,
	}
}

type apiBindingAccessAuthorizer struct {
	versionedInformers clientgoinformers.SharedInformerFactory
	apiBindingIndexer  cache.Indexer
	delegate           authorizer.Authorizer
}

func (a *apiBindingAccessAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	apiBindingAccessDenied := "bound api access is not permitted"

	// get the cluster from the ctx.
	lcluster, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return authorizer.DecisionNoOpinion, apiBindingAccessDenied, err
	}

	bindingLogicalCluster, bound, err := a.getAPIBindingWorkspace(attr, lcluster)
	if err != nil {
		return authorizer.DecisionNoOpinion, apiBindingAccessDenied, err
	}

	if !bound {
		return a.delegate.Authorize(ctx, attr)
	}

	// If bound, create a rbac authorizer filtered to the cluster.
	clusterKubeInformer := rbacwrapper.FilterInformers(bindingLogicalCluster, a.versionedInformers.Rbac().V1())
	clusterAuthorizer := rbac.New(
		&rbac.RoleGetter{Lister: clusterKubeInformer.Roles().Lister()},
		&rbac.RoleBindingLister{Lister: clusterKubeInformer.RoleBindings().Lister()},
		&rbac.ClusterRoleGetter{Lister: clusterKubeInformer.ClusterRoles().Lister()},
		&rbac.ClusterRoleBindingLister{Lister: clusterKubeInformer.ClusterRoleBindings().Lister()},
	)
	dec, reason, err := clusterAuthorizer.Authorize(ctx, attr)
	if err != nil {
		return authorizer.DecisionNoOpinion, reason, err
	}

	if dec == authorizer.DecisionAllow {
		return a.delegate.Authorize(ctx, attr)
	}

	return authorizer.DecisionNoOpinion, reason, nil
}

//TODO [shawn-hurley]: this should be a helper shared.
func (a *apiBindingAccessAuthorizer) getAPIBindingWorkspace(attr authorizer.Attributes, clusterName logicalcluster.Name) (logicalcluster.Name, bool, error) {
	parentClusterName, hasParent := clusterName.Parent()
	if !hasParent {
		// APIBindings in root are not possible (they can only point to sibling workspaces).
		return logicalcluster.New(""), false, nil
	}

	objs, err := a.apiBindingIndexer.ByIndex(byWorkspaceIndex, clusterName.String())
	if err != nil {
		return logicalcluster.New(""), false, err
	}
	for _, obj := range objs {
		apiBinding := obj.(*apisv1alpha1.APIBinding)
		for _, br := range apiBinding.Status.BoundResources {
			if apiBinding.Status.BoundAPIExport.Workspace == nil {
				// this will never happen today. But as soon as we add other reference types (like exports), this log output will remind out of necessary work here.
				continue
			}
			if br.Group == attr.GetAPIGroup() && br.Resource == attr.GetResource() {
				return parentClusterName.Join(apiBinding.Status.BoundAPIExport.Workspace.WorkspaceName), true, nil
			}
		}
	}
	return logicalcluster.New(""), false, nil
}
