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
	"fmt"

	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
	controlplaneapiserver "k8s.io/kubernetes/pkg/controlplane/apiserver"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	"github.com/kcp-dev/kcp/pkg/indexers"
	rbacwrapper "github.com/kcp-dev/kcp/pkg/virtual/framework/wrappers/rbac"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
)

const (
	MaximalPermissionPolicyAccessNotPermittedReason = "access not permitted by maximal permission policy"
)

// NewMaximalPermissionPolicyAuthorizer returns an authorizer that first checks if the request is for a
// bound resource or not. If the resource is bound it checks the maximal permission policy of the underlying API export.
func NewMaximalPermissionPolicyAuthorizer(kubeInformers, globalKubeInformers kcpkubernetesinformers.SharedInformerFactory, kcpInformers, globalKcpInformers kcpinformers.SharedInformerFactory, delegate authorizer.Authorizer) authorizer.Authorizer {
	// Make sure informer knows what to watch
	kubeInformers.Rbac().V1().Roles().Lister()
	kubeInformers.Rbac().V1().RoleBindings().Lister()
	kubeInformers.Rbac().V1().ClusterRoles().Lister()
	kubeInformers.Rbac().V1().ClusterRoleBindings().Lister()

	globalKubeInformers.Rbac().V1().Roles().Lister()
	globalKubeInformers.Rbac().V1().RoleBindings().Lister()
	globalKubeInformers.Rbac().V1().ClusterRoles().Lister()
	globalKubeInformers.Rbac().V1().ClusterRoleBindings().Lister()

	indexers.AddIfNotPresentOrDie(kcpInformers.Apis().V1alpha1().APIExports().Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})

	indexers.AddIfNotPresentOrDie(globalKcpInformers.Apis().V1alpha1().APIExports().Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})

	return &MaximalPermissionPolicyAuthorizer{
		getAPIBindings: func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error) {
			return kcpInformers.Apis().V1alpha1().APIBindings().Lister().Cluster(clusterName).List(labels.Everything())
		},
		getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
			return indexers.ByPathAndNameWithFallback[*apisv1alpha1.APIExport](apisv1alpha1.Resource("apiexports"), kcpInformers.Apis().V1alpha1().APIExports().Informer().GetIndexer(), globalKcpInformers.Apis().V1alpha1().APIExports().Informer().GetIndexer(), path, name)
		},
		newAuthorizer: func(clusterName logicalcluster.Name) authorizer.Authorizer {
			return rbac.New(
				&rbac.RoleGetter{Lister: rbacwrapper.NewMergedRoleLister(
					kubeInformers.Rbac().V1().Roles().Lister().Cluster(clusterName),
					globalKubeInformers.Rbac().V1().Roles().Lister().Cluster(clusterName),
					kubeInformers.Rbac().V1().Roles().Lister().Cluster(controlplaneapiserver.LocalAdminCluster),
				)},
				&rbac.RoleBindingLister{Lister: rbacwrapper.NewMergedRoleBindingLister(
					kubeInformers.Rbac().V1().RoleBindings().Lister().Cluster(clusterName),
					globalKubeInformers.Rbac().V1().RoleBindings().Lister().Cluster(clusterName),
				)},
				&rbac.ClusterRoleGetter{Lister: rbacwrapper.NewMergedClusterRoleLister(
					kubeInformers.Rbac().V1().ClusterRoles().Lister().Cluster(clusterName),
					globalKubeInformers.Rbac().V1().ClusterRoles().Lister().Cluster(clusterName),
					kubeInformers.Rbac().V1().ClusterRoles().Lister().Cluster(controlplaneapiserver.LocalAdminCluster),
				)},
				&rbac.ClusterRoleBindingLister{Lister: rbacwrapper.NewMergedClusterRoleBindingLister(
					kubeInformers.Rbac().V1().ClusterRoleBindings().Lister().Cluster(clusterName),
					globalKubeInformers.Rbac().V1().ClusterRoleBindings().Lister().Cluster(clusterName),
					kubeInformers.Rbac().V1().ClusterRoleBindings().Lister().Cluster(controlplaneapiserver.LocalAdminCluster),
				)},
			)
		},
		delegate: delegate,
	}
}

type MaximalPermissionPolicyAuthorizer struct {
	getAPIBindings func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error)
	getAPIExport   func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error)

	newAuthorizer func(clusterName logicalcluster.Name) authorizer.Authorizer

	delegate authorizer.Authorizer
}

func (a *MaximalPermissionPolicyAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	if IsDeepSubjectAccessReviewFrom(ctx, attr) {
		// this is a deep SAR request, we have to skip the checks here and delegate to the subsequent authorizer.
		return DelegateAuthorization("deep SAR request", a.delegate).Authorize(ctx, attr)
	}

	// get the cluster from the ctx.
	lcluster, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return authorizer.DecisionNoOpinion, "", fmt.Errorf("error getting cluster from request: %w", err)
	}

	// find a binding that provides the requested resource
	bindings, err := a.getAPIBindings(lcluster)
	if err != nil {
		return authorizer.DecisionNoOpinion, MaximalPermissionPolicyAccessNotPermittedReason, fmt.Errorf("error getting APIBindings: %w", err)
	}
	var relevantBinding *apisv1alpha1.APIBinding
	for _, binding := range bindings {
		for _, br := range binding.Status.BoundResources {
			if br.Group == attr.GetAPIGroup() && br.Resource == attr.GetResource() {
				relevantBinding = binding
				break
			}
		}
	}
	if relevantBinding == nil {
		return DelegateAuthorization("no relevant binding found", a.delegate).Authorize(ctx, attr)
	}

	// get the corresponding APIExport
	path := logicalcluster.NewPath(relevantBinding.Spec.Reference.Export.Path)
	if path.Empty() {
		path = lcluster.Path()
	}
	apiExport, err := a.getAPIExport(path, relevantBinding.Spec.Reference.Export.Name)
	if err != nil && !apierrors.IsNotFound(err) {
		return authorizer.DecisionNoOpinion, MaximalPermissionPolicyAccessNotPermittedReason, fmt.Errorf("error getting API export: %w", err)
	} else if apierrors.IsNotFound(err) {
		// If we can't find the export default to close
		return authorizer.DecisionNoOpinion, fmt.Sprintf("APIExport %q:%q not found", path, relevantBinding.Spec.Reference.Export.Name), err
	}

	if apiExport.Spec.MaximalPermissionPolicy == nil {
		return DelegateAuthorization(fmt.Sprintf("no maximum permission policy in API Export %q|%q", logicalcluster.From(apiExport), apiExport.Name), a.delegate).Authorize(ctx, attr)
	}

	if apiExport.Spec.MaximalPermissionPolicy.Local == nil {
		return DelegateAuthorization(fmt.Sprintf("no local maximum permission policy in API Export %q|%q", logicalcluster.From(apiExport), apiExport.Name), a.delegate).Authorize(ctx, attr)
	}

	// If bound, create a rbac authorizer filtered to the cluster.
	clusterAuthorizer := a.newAuthorizer(logicalcluster.From(apiExport))
	prefixedAttr := deepCopyAttributes(attr)
	userInfo := prefixedAttr.User.(*user.DefaultInfo)
	userInfo.Name = apisv1alpha1.MaximalPermissionPolicyRBACUserGroupPrefix + userInfo.Name
	userInfo.Groups = make([]string, 0, len(attr.GetUser().GetGroups()))
	for _, g := range attr.GetUser().GetGroups() {
		userInfo.Groups = append(userInfo.Groups, apisv1alpha1.MaximalPermissionPolicyRBACUserGroupPrefix+g)
	}
	dec, reason, err := clusterAuthorizer.Authorize(ctx, prefixedAttr)
	reason = fmt.Sprintf("API export %q|%q policy: %v", logicalcluster.From(apiExport), apiExport.Name, reason)
	if err != nil {
		return authorizer.DecisionNoOpinion, reason, fmt.Errorf("error authorizing API export cluster RBAC policy: %w", err)
	}
	if dec == authorizer.DecisionAllow {
		return DelegateAuthorization(reason, a.delegate).Authorize(ctx, attr)
	}
	return authorizer.DecisionNoOpinion, reason, nil
}

func deepCopyAttributes(attr authorizer.Attributes) authorizer.AttributesRecord {
	return authorizer.AttributesRecord{
		User: &user.DefaultInfo{
			Name:   attr.GetUser().GetName(),
			UID:    attr.GetUser().GetUID(),
			Groups: attr.GetUser().GetGroups(),
			Extra:  attr.GetUser().GetExtra(),
		},
		Verb:            attr.GetVerb(),
		Namespace:       attr.GetNamespace(),
		APIGroup:        attr.GetAPIGroup(),
		APIVersion:      attr.GetAPIVersion(),
		Resource:        attr.GetResource(),
		Subresource:     attr.GetSubresource(),
		Name:            attr.GetName(),
		ResourceRequest: attr.IsResourceRequest(),
		Path:            attr.GetPath(),
	}
}
