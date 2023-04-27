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

package server

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	rbaclisters "github.com/kcp-dev/client-go/listers/rbac/v1"
	"github.com/kcp-dev/logicalcluster/v3"

	rbacv1 "k8s.io/api/rbac/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/handlers/negotiation"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/admission/workspace"
	"github.com/kcp-dev/kcp/pkg/admission/workspacetypeexists"
	"github.com/kcp-dev/kcp/pkg/authorization"
	indexrewriters "github.com/kcp-dev/kcp/pkg/index/rewriters"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	reconcilerworkspace "github.com/kcp-dev/kcp/pkg/reconciler/tenancy/workspace"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
	corev1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/core/v1alpha1"
	tenancyv1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/tenancy/v1alpha1"
)

var (
	homeWorkspaceScheme = runtime.NewScheme()
	homeWorkspaceCodecs = serializer.NewCodecFactory(homeWorkspaceScheme)
)

func init() {
	_ = tenancyv1alpha1.AddToScheme(homeWorkspaceScheme)
}

// WithHomeWorkspaces implements an HTTP handler, in the KCP server, which:
//
//   - supports a special 'kubectl get workspace ~' request which returns either
//     the old bucket-style workspace if it exists (= a LogicalCluster can be found)
//     or a new parent-less home workspace. It will create the latter on the fly.
//
// When the Home workspace is still not Ready, the handler returns a Retry-After
// response with a delay in seconds that is configurable (creationDelaySeconds),
// so that client-go clients will automatically retry the request after this delay.
//
// To find old bucket-style home workspaces, the following bucket parameters still apply:
// - homePrefix is the workspace that will contains all the user home workspaces, partitioned by bucket workspaces
// - bucketLevels is the number of bucket workspaces met before reaching a home workspace from the homePefix workspace
// - bucketSize is the number of chars comprising each bucket.
//
// Bucket workspace names are calculated based on the user name hash.
func WithHomeWorkspaces(
	apiHandler http.Handler,
	a authorizer.Authorizer,
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	kcpClusterClient kcpclientset.ClusterInterface,
	kubeSharedInformerFactory kcpkubernetesinformers.SharedInformerFactory,
	kcpSharedInformerFactory kcpinformers.SharedInformerFactory,
	externalHost string,
) (http.Handler, error) {
	h := &homeWorkspaceHandler{
		delegate: apiHandler,

		authz: a,

		creationTimeout: time.Minute,
		externalHost:    externalHost,

		kcpClusterClient:  kcpClusterClient,
		kubeClusterClient: kubeClusterClient,

		logicalClusterLister:  kcpSharedInformerFactory.Core().V1alpha1().LogicalClusters().Lister(),
		logicalClusterIndexer: kcpSharedInformerFactory.Core().V1alpha1().LogicalClusters().Informer().GetIndexer(),

		clusterRoleBindingLister:  kubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings().Lister(),
		clusterRoleBindingIndexer: kubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings().Informer().GetIndexer(),

		workspaceTypeLister:  kcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceTypes().Lister(),
		workspaceTypeIndexer: kcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceTypes().Informer().GetIndexer(),

		hasSynced: kcpSharedInformerFactory.Core().V1alpha1().LogicalClusters().Informer().HasSynced,
	}

	h.transitiveTypeResolver = workspacetypeexists.NewTransitiveTypeResolver(h.getWorkspaceType)

	return h, nil
}

type homeWorkspaceHandler struct {
	delegate http.Handler

	authz authorizer.Authorizer

	creationTimeout time.Duration
	externalHost    string

	transitiveTypeResolver workspacetypeexists.TransitiveTypeResolver

	kcpClusterClient  kcpclientset.ClusterInterface
	kubeClusterClient kcpkubernetesclientset.ClusterInterface

	logicalClusterLister  corev1alpha1listers.LogicalClusterClusterLister
	logicalClusterIndexer cache.Indexer

	clusterRoleBindingLister  rbaclisters.ClusterRoleBindingClusterLister
	clusterRoleBindingIndexer cache.Indexer

	workspaceTypeLister  tenancyv1alpha1listers.WorkspaceTypeClusterLister
	workspaceTypeIndexer cache.Indexer

	hasSynced func() bool
}

func (h *homeWorkspaceHandler) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	logger := klog.FromContext(ctx)
	lcluster := request.ClusterFrom(req.Context())
	if lcluster == nil {
		// this is not a home workspace request
		// just pass it to the next handler
		h.delegate.ServeHTTP(rw, req)
		return
	}
	logger = logging.WithCluster(logger, lcluster)
	effectiveUser, ok := request.UserFrom(ctx)
	if !ok {
		err := errors.New("no user in HomeWorkspaces filter")
		responsewriters.InternalError(rw, req, err)
		return
	}
	logger = logging.WithUser(logger, effectiveUser)
	requestInfo, ok := request.RequestInfoFrom(ctx)
	if !ok {
		responsewriters.InternalError(rw, req, errors.New("no request Info"))
		return
	}
	// TODO:(p0lyn0mial): the following checks are temporary only to get the phase A merged
	if requestInfo.IsResourceRequest && requestInfo.APIGroup == tenancyv1alpha1.SchemeGroupVersion.Group && requestInfo.Resource == "logicalclusters" {
		h.delegate.ServeHTTP(rw, req)
		return
	}
	if requestInfo.IsResourceRequest && requestInfo.APIGroup == rbacv1.SchemeGroupVersion.Group && requestInfo.Resource == "clusterrolebindings" {
		h.delegate.ServeHTTP(rw, req)
		return
	}

	if !isGetHomeWorkspaceRequest(lcluster.Name, requestInfo) {
		// this is not a home workspace request
		// just pass it to the next handler
		h.delegate.ServeHTTP(rw, req)
		return
	}

	if !h.hasSynced() {
		responsewriters.InternalError(rw, req, errors.New("cache not synced"))
		return
	}

	homeClusterName := indexrewriters.HomeClusterName(effectiveUser.GetName())
	logicalCluster, err := h.logicalClusterLister.Cluster(homeClusterName).Get(corev1alpha1.LogicalClusterName)
	if err != nil {
		if !kerrors.IsNotFound(err) {
			responsewriters.InternalError(rw, req, err)
			return
		}
		// check permissions first
		attr := authorizer.AttributesRecord{
			User:            effectiveUser,
			Verb:            "create",
			APIGroup:        tenancyv1alpha1.SchemeGroupVersion.Group,
			Resource:        "workspaces",
			Name:            "~",
			ResourceRequest: true,
		}
		decision, _, err := h.authz.Authorize(ctx, attr)
		if err != nil {
			logger.WithValues("cluster", homeClusterName, "user", effectiveUser.GetName()).Error(err, "error authorizing request")
			responsewriters.Forbidden(ctx, attr, rw, req, authorization.WorkspaceAccessNotPermittedReason, homeWorkspaceCodecs)
			return
		}
		if decision != authorizer.DecisionAllow {
			responsewriters.Forbidden(ctx, attr, rw, req, authorization.WorkspaceAccessNotPermittedReason, homeWorkspaceCodecs)
			return
		}

		userInfo, err := workspace.WorkspaceOwnerAnnotationValue(effectiveUser)
		if err != nil {
			responsewriters.InternalError(rw, req, err)
			return
		}

		logicalCluster = &corev1alpha1.LogicalCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: corev1alpha1.LogicalClusterName,
				Annotations: map[string]string{
					tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey: userInfo,
					tenancyv1alpha1.LogicalClusterTypeAnnotationKey:         "root:home",
					core.LogicalClusterPathAnnotationKey:                    fmt.Sprintf("user:%s", effectiveUser.GetName()),
				},
			},
		}
		logicalCluster.Spec.Initializers, err = reconcilerworkspace.LogicalClustersInitializers(h.transitiveTypeResolver, h.getWorkspaceType, core.RootCluster.Path(), "home")
		if err != nil {
			responsewriters.InternalError(rw, req, err)
			return
		}

		logger.Info("Creating home LogicalCluster", "cluster", homeClusterName.String(), "user", effectiveUser.GetName())
		logicalCluster, err = h.kcpClusterClient.Cluster(homeClusterName.Path()).CoreV1alpha1().LogicalClusters().Create(ctx, logicalCluster, metav1.CreateOptions{})
		if err != nil && !kerrors.IsAlreadyExists(err) {
			responsewriters.InternalError(rw, req, err)
			return
		}
	}

	const retrySeconds = "1"

	// here we have a LogicalCluster. Create ClusterRoleBinding. Again: if this is pre-existing
	// and it is not belonging to the current user, the user will get a 403 through normal authorization.

	if logicalCluster.Status.Phase == corev1alpha1.LogicalClusterPhaseScheduling {
		logger.Info("Creating home ClusterRoleBinding", "cluster", homeClusterName.String(), "user", effectiveUser.GetName(), "name", "workspace-admin")
		_, err := h.kubeClusterClient.Cluster(homeClusterName.Path()).RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: "workspace-admin",
			},
			Subjects: []rbacv1.Subject{
				{
					APIGroup: rbacv1.GroupName,
					Kind:     "User",
					Name:     effectiveUser.GetName(),
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     "cluster-admin",
			},
		}, metav1.CreateOptions{})
		if err != nil && !kerrors.IsAlreadyExists(err) {
			responsewriters.InternalError(rw, req, err)
			return
		}

		// move to Initializing state
		logicalCluster = logicalCluster.DeepCopy()
		logicalCluster.Status.Phase = corev1alpha1.LogicalClusterPhaseInitializing
		logicalCluster, err = h.kcpClusterClient.Cluster(homeClusterName.Path()).CoreV1alpha1().LogicalClusters().UpdateStatus(ctx, logicalCluster, metav1.UpdateOptions{})
		if err != nil {
			if kerrors.IsConflict(err) {
				rw.Header().Set("Retry-After", retrySeconds)
				http.Error(rw, "Creating the home workspace", http.StatusTooManyRequests)
				return
			}
			responsewriters.InternalError(rw, req, err)
			return
		}
	}

	if logicalCluster.Status.Phase == corev1alpha1.LogicalClusterPhaseInitializing {
		if time.Since(logicalCluster.CreationTimestamp.Time) > h.creationTimeout {
			responsewriters.InternalError(rw, req, fmt.Errorf("home workspace creation timeout"))
			return
		}

		rw.Header().Set("Retry-After", retrySeconds)
		http.Error(rw, "Creating the home workspace", http.StatusTooManyRequests)
		return
	}

	// here we have a LogicalCluster in the Running state.

	homeWorkspace := &tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:              logicalCluster.Name,
			CreationTimestamp: logicalCluster.CreationTimestamp,
		},
		Spec: tenancyv1alpha1.WorkspaceSpec{
			Cluster: logicalcluster.From(logicalCluster).String(),
			URL:     logicalCluster.Status.URL,
		},
		Status: tenancyv1alpha1.WorkspaceStatus{
			Phase:        logicalCluster.Status.Phase,
			Conditions:   logicalCluster.Status.Conditions,
			Initializers: logicalCluster.Status.Initializers,
		},
	}
	responsewriters.WriteObjectNegotiated(homeWorkspaceCodecs, negotiation.DefaultEndpointRestrictions, tenancyv1alpha1.SchemeGroupVersion, rw, req, http.StatusOK, homeWorkspace, false)
}

func (h *homeWorkspaceHandler) getWorkspaceType(path logicalcluster.Path, name string) (*tenancyv1alpha1.WorkspaceType, error) {
	return indexers.ByPathAndName[*tenancyv1alpha1.WorkspaceType](tenancyv1alpha1.Resource("workspacetypes"), h.workspaceTypeIndexer, path, name)
}

func isGetHomeWorkspaceRequest(clusterName logicalcluster.Name, requestInfo *request.RequestInfo) bool {
	return clusterName == core.RootCluster &&
		requestInfo.IsResourceRequest &&
		requestInfo.Verb == "get" &&
		requestInfo.APIGroup == tenancyv1alpha1.SchemeGroupVersion.Group &&
		requestInfo.Resource == "workspaces" &&
		requestInfo.Name == "~"
}
