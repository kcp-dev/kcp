/*
Copyright 2025 The kcp Authors.

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

package clustercachedresourceendpointslice

import (
	"context"
	"fmt"
	"io"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"

	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	"github.com/kcp-dev/kcp/pkg/indexers"
)

const PluginName = "cache.kcp.io/ClusterCachedResourceEndpointSlice"

// Ensure that the required admission interfaces are implemented.
var (
	_ = admission.ValidationInterface(&Admission{})
	_ = admission.InitializationValidator(&Admission{})
	_ = kcpinitializers.WantsKcpInformers(&Admission{})
	_ = kcpinitializers.WantsDeepSARClient(&Admission{})
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			p := &Admission{
				Handler:          admission.NewHandler(admission.Create),
				createAuthorizer: delegated.NewDelegatedAuthorizer,
			}
			p.getClusterCachedResource = func(path logicalcluster.Path, name string) (*cachev1alpha1.ClusterCachedResource, error) {
				return indexers.ByPathAndNameWithFallback[*cachev1alpha1.ClusterCachedResource](
					cachev1alpha1.Resource("clustercachedresources"), p.localIndexer, p.globalIndexer, path, name)
			}
			return p, nil
		})
}

// Admission checks that the creator of a ClusterCachedResourceEndpointSlice has the "bind" verb
// on the referenced ClusterCachedResource in the workspace that ClusterCachedResource lives in.
// Without this, anybody could reference a ClusterCachedResource in a foreign workspace and read
// the objects it replicates through the replication virtual workspace. The reference is immutable
// (CEL validation), so checking on create is sufficient.
type Admission struct {
	*admission.Handler

	getClusterCachedResource func(path logicalcluster.Path, name string) (*cachev1alpha1.ClusterCachedResource, error)
	localIndexer             cache.Indexer
	globalIndexer            cache.Indexer

	deepSARClient    kcpkubernetesclientset.ClusterInterface
	createAuthorizer delegated.DelegatedAuthorizerFactory
}

func (adm *Admission) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	if a.GetResource().GroupResource() != cachev1alpha1.Resource("clustercachedresourceendpointslices") || a.GetOperation() != admission.Create {
		return nil
	}

	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}
	slice := &cachev1alpha1.ClusterCachedResourceEndpointSlice{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, slice); err != nil {
		return fmt.Errorf("failed to convert unstructured to ClusterCachedResourceEndpointSlice: %w", err)
	}
	ref := slice.Spec.ClusterCachedResource
	if ref.Name == "" {
		return admission.NewForbidden(a, field.Required(field.NewPath("spec", "clusterCachedResource", "name"), ""))
	}

	// Unified forbidden error that does not leak workspace or object existence.
	forbidden := admission.NewForbidden(a, fmt.Errorf("no permission to bind to ClusterCachedResource %s",
		logicalcluster.NewPath(ref.Path).Join(ref.Name).String()))

	// A same-workspace reference does not need the object to exist. A path reference is resolved
	// through the (always replicated) ClusterCachedResource, like APIExportEndpointSlice does for exports.
	targetCluster := clusterName
	if ref.Path != "" {
		clusterCachedResource, err := adm.getClusterCachedResource(logicalcluster.NewPath(ref.Path), ref.Name)
		if err != nil {
			return forbidden
		}
		targetCluster = logicalcluster.From(clusterCachedResource)
	}

	authz, err := adm.createAuthorizer(targetCluster, adm.deepSARClient, delegated.Options{})
	if err != nil {
		klog.FromContext(ctx).Error(err, "error creating authorizer from delegating authorizer config")
		return forbidden
	}
	decision, _, err := authz.Authorize(ctx, authorizer.AttributesRecord{
		User:            a.GetUserInfo(),
		Verb:            "bind",
		APIGroup:        cachev1alpha1.SchemeGroupVersion.Group,
		APIVersion:      cachev1alpha1.SchemeGroupVersion.Version,
		Resource:        "clustercachedresources",
		Name:            ref.Name,
		ResourceRequest: true,
	})
	if err != nil || decision != authorizer.DecisionAllow {
		return forbidden
	}

	return nil
}

func (adm *Admission) ValidateInitialization() error {
	if adm.localIndexer == nil || adm.globalIndexer == nil {
		return fmt.Errorf(PluginName + " plugin needs ClusterCachedResource indexers")
	}
	if adm.deepSARClient == nil {
		return fmt.Errorf(PluginName + " plugin needs a deepSARClient")
	}
	return nil
}

func (adm *Admission) SetKcpInformers(local, global kcpinformers.SharedInformerFactory) {
	localSynced := local.Cache().V1alpha1().ClusterCachedResources().Informer().HasSynced
	globalSynced := global.Cache().V1alpha1().ClusterCachedResources().Informer().HasSynced
	adm.SetReadyFunc(func() bool { return localSynced() && globalSynced() })

	adm.localIndexer = local.Cache().V1alpha1().ClusterCachedResources().Informer().GetIndexer()
	adm.globalIndexer = global.Cache().V1alpha1().ClusterCachedResources().Informer().GetIndexer()
	for _, idx := range []cache.Indexer{adm.localIndexer, adm.globalIndexer} {
		indexers.AddIfNotPresentOrDie(idx, cache.Indexers{
			indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
		})
	}
}

func (adm *Admission) SetDeepSARClient(client kcpkubernetesclientset.ClusterInterface) {
	adm.deepSARClient = client
}
