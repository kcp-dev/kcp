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
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/kcp-dev/logicalcluster/v2"

	authenticationv1 "k8s.io/api/authentication/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	kuser "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/handlers/negotiation"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
	coreexternalversions "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	clusterworkspaceadmission "github.com/kcp-dev/kcp/pkg/admission/clusterworkspace"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/projection"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	"github.com/kcp-dev/kcp/pkg/authorization"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpexternalversions "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/softimpersonation"
)

const (
	homeOwnerClusterRolePrefix     = "system:kcp:tenancy:home-owner:"
	HomeBucketClusterWorkspaceType = "homebucket"
	HomeClusterWorkspaceType       = "home"
)

var (
	homeWorkspaceScheme = runtime.NewScheme()
	homeWorkspaceCodecs = serializer.NewCodecFactory(homeWorkspaceScheme)
)

func init() {
	_ = tenancyv1beta1.AddToScheme(homeWorkspaceScheme)
}

// WithHomeWorkspaces implements an HTTP handler, in the KCP server, which:
//
// - creates a Home workspace on-demand for requests that target the home workspace or its descendants,
// taking care of the optional creation of bucket workspaces,
// - supports a special 'kubectl get workspace ~' request which can return the user home workspace definition even before it exists.
//
// When the Home workspace is still not Ready, the handler returns a Retry-After response with a delay in seconds that is configurable
// (creationDelaySeconds), so that client-go clients will automatically retry the request after this delay.
//
// - homePrefix is the workspace that will contains all the user home workspaces, partitioned by bucket workspaces
// - bucketLevels is the number of bucket workspaces met before reaching a home workspace from the homePefix workspace
// - bucketSize is the number of chars comprising each bucket.
//
// Bucket workspace names are calculated based on the user name hash.
func WithHomeWorkspaces(
	apiHandler http.Handler,
	a authorizer.Authorizer,
	kubeClusterClient kubernetes.ClusterInterface,
	kcpClusterClient kcpclient.ClusterInterface,
	kubeSharedInformerFactory coreexternalversions.SharedInformerFactory,
	kcpSharedInformerFactory kcpexternalversions.SharedInformerFactory,
	externalHost string,
	creationDelaySeconds int,
	homePrefix logicalcluster.Name,
	bucketLevels,
	bucketSize int,
) http.Handler {
	if bucketLevels > 5 || bucketSize > 4 {
		panic("bucketLevels and bucketSize must be <= 5 and <= 4")
	}
	return homeWorkspaceHandlerBuilder{
		apiHandler:           apiHandler,
		externalHost:         externalHost,
		authz:                a,
		creationDelaySeconds: creationDelaySeconds,
		homePrefix:           homePrefix,
		bucketLevels:         bucketLevels,
		bucketSize:           bucketSize,
		kcp:                  buildExternalClientsAccess(kubeClusterClient, kcpClusterClient),
		localInformers:       buildLocalInformersAccess(kubeSharedInformerFactory, kcpSharedInformerFactory),
	}.build()
}

type externalKubeClientsAccess struct {
	createClusterWorkspace   func(ctx context.Context, lcluster logicalcluster.Name, cw *tenancyv1alpha1.ClusterWorkspace) error
	getClusterWorkspace      func(ctx context.Context, lcluster logicalcluster.Name, name string) (*tenancyv1alpha1.ClusterWorkspace, error)
	createClusterRole        func(ctx context.Context, lcluster logicalcluster.Name, cr *rbacv1.ClusterRole) error
	createClusterRoleBinding func(ctx context.Context, lcluster logicalcluster.Name, crb *rbacv1.ClusterRoleBinding) error
}

func buildExternalClientsAccess(kubeClusterClient kubernetes.ClusterInterface, kcpClusterClient kcpclient.ClusterInterface) externalKubeClientsAccess {
	return externalKubeClientsAccess{
		createClusterRole: func(ctx context.Context, workspace logicalcluster.Name, cr *rbacv1.ClusterRole) error {
			_, err := kubeClusterClient.Cluster(workspace).RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
			return err
		},
		createClusterRoleBinding: func(ctx context.Context, workspace logicalcluster.Name, crb *rbacv1.ClusterRoleBinding) error {
			_, err := kubeClusterClient.Cluster(workspace).RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
			return err
		},
		createClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, cw *tenancyv1alpha1.ClusterWorkspace) error {
			_, err := kcpClusterClient.Cluster(workspace).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, cw, metav1.CreateOptions{})
			return err
		},
		getClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, name string) (*tenancyv1alpha1.ClusterWorkspace, error) {
			return kcpClusterClient.Cluster(workspace).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, name, metav1.GetOptions{})
		},
	}
}

type localInformersAccess struct {
	getClusterWorkspace   func(logicalcluster.Name) (*tenancyv1alpha1.ClusterWorkspace, error)
	getClusterRole        func(lcluster logicalcluster.Name, name string) (*rbacv1.ClusterRole, error)
	getClusterRoleBinding func(lcluster logicalcluster.Name, name string) (*rbacv1.ClusterRoleBinding, error)
	synced                func() bool
}

func buildLocalInformersAccess(kubeSharedInformerFactory coreexternalversions.SharedInformerFactory, kcpSharedInformerFactory kcpexternalversions.SharedInformerFactory) localInformersAccess {
	clusterWorkspaceInformer := kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces().Informer()
	crInformer := kubeSharedInformerFactory.Rbac().V1().ClusterRoles().Informer()
	crbInformer := kubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings().Informer()
	clusterWorkspaceLister := kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces().Lister()
	crLister := kubeSharedInformerFactory.Rbac().V1().ClusterRoles().Lister()
	crbLister := kubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings().Lister()

	return localInformersAccess{
		getClusterWorkspace: func(logicalCluster logicalcluster.Name) (*tenancyv1alpha1.ClusterWorkspace, error) {
			parentLogicalCluster, workspaceName := logicalCluster.Split()
			return clusterWorkspaceLister.Get(clusters.ToClusterAwareKey(parentLogicalCluster, workspaceName))
		},
		getClusterRole: func(workspace logicalcluster.Name, name string) (*rbacv1.ClusterRole, error) {
			return crLister.Get(clusters.ToClusterAwareKey(workspace, name))
		},
		getClusterRoleBinding: func(workspace logicalcluster.Name, name string) (*rbacv1.ClusterRoleBinding, error) {
			return crbLister.Get(clusters.ToClusterAwareKey(workspace, name))
		},
		synced: func() bool {
			return clusterWorkspaceInformer.HasSynced() &&
				crInformer.HasSynced() &&
				crbInformer.HasSynced()
		},
	}
}

type homeWorkspaceHandlerBuilder struct {
	apiHandler http.Handler

	externalHost             string
	homePrefix               logicalcluster.Name
	bucketLevels, bucketSize int
	creationDelaySeconds     int

	authz authorizer.Authorizer

	kcp            externalKubeClientsAccess
	localInformers localInformersAccess
}

type homeWorkspaceFeatureLogic struct {
	searchForHomeWorkspaceRBACResourcesInLocalInformers func(homeWorkspace logicalcluster.Name) (found bool, err error)
	createHomeWorkspaceRBACResources                    func(ctx context.Context, userName string, homeWorkspace logicalcluster.Name) error
	searchForWorkspaceAndRBACInLocalInformers           func(logicalClusterName logicalcluster.Name, isHome bool, userName string) (readyAndRBACAsExpected bool, retryAfterSeconds int, checkError error)
	tryToCreate                                         func(ctx context.Context, user kuser.Info, workspaceToCheck logicalcluster.Name, workspaceType tenancyv1alpha1.ClusterWorkspaceTypeName) (retryAfterSeconds int, createError error)
}

type homeWorkspaceHandler struct {
	homeWorkspaceHandlerBuilder
	homeWorkspaceFeatureLogic
}

func (b homeWorkspaceHandlerBuilder) build() *homeWorkspaceHandler {
	h := &homeWorkspaceHandler{}
	h.homeWorkspaceHandlerBuilder = b
	h.homeWorkspaceFeatureLogic = homeWorkspaceFeatureLogic{
		searchForHomeWorkspaceRBACResourcesInLocalInformers: func(logicalClusterName logicalcluster.Name) (found bool, err error) {
			return searchForHomeWorkspaceRBACResourcesInLocalInformers(h, logicalClusterName)
		},
		createHomeWorkspaceRBACResources: func(ctx context.Context, userName string, logicalClusterName logicalcluster.Name) error {
			return createHomeWorkspaceRBACResources(h, ctx, userName, logicalClusterName)
		},
		searchForWorkspaceAndRBACInLocalInformers: func(logicalClusterName logicalcluster.Name, isHome bool, userName string) (found bool, retryAfterSeconds int, checkError error) {
			return searchForWorkspaceAndRBACInLocalInformers(h, logicalClusterName, isHome, userName)
		},
		tryToCreate: func(ctx context.Context, user kuser.Info, logicalClusterName logicalcluster.Name, workspaceType tenancyv1alpha1.ClusterWorkspaceTypeName) (retryAfterSeconds int, createError error) {
			return tryToCreate(h, ctx, user, logicalClusterName, workspaceType)
		},
	}

	return h
}

func isGetHomeWorkspaceRequest(clusterName logicalcluster.Name, requestInfo *request.RequestInfo) bool {
	return clusterName == tenancyv1alpha1.RootCluster &&
		requestInfo.IsResourceRequest &&
		requestInfo.Verb == "get" &&
		requestInfo.APIGroup == tenancyv1beta1.SchemeGroupVersion.Group &&
		requestInfo.Resource == "workspaces" &&
		requestInfo.Name == "~"
}

func (h *homeWorkspaceHandler) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	if !h.localInformers.synced() {
		h.apiHandler.ServeHTTP(rw, req)
		return
	}

	ctx := req.Context()
	lcluster, err := request.ValidClusterFrom(ctx)
	if err != nil {
		responsewriters.InternalError(rw, req, err)
		return
	}
	effectiveUser, ok := request.UserFrom(ctx)
	if !ok {
		err := errors.New("No user in HomeWorkspaces filter !")
		responsewriters.InternalError(rw, req, err)
		return
	}
	if sets.NewString(effectiveUser.GetGroups()...).Has(kuser.SystemPrivilegedGroup) {
		// If we are the system privileged group, it might be a call from the virtual workspace
		// in which case we also search the user in the soft impersonation header.
		if impersonated, err := softimpersonation.UserInfoFromRequestHeader(req); err != nil {
			responsewriters.InternalError(rw, req, err)
			return
		} else if impersonated != nil {
			effectiveUser = impersonated
		}
	}
	requestInfo, ok := request.RequestInfoFrom(ctx)
	if !ok {
		responsewriters.InternalError(rw, req, errors.New("no request Info"))
		return
	}

	var workspaceType tenancyv1alpha1.ClusterWorkspaceTypeName
	if isGetHomeWorkspaceRequest(lcluster.Name, requestInfo) {
		// if we are in the special case of a `kubectl get workspace ~` request (against the 'root' workspace),
		// we return the Home workspace definition of the current user, possibly even before its
		// underlying ClusterWorkspace resource exists.

		getAttributes := homeWorkspaceAuthorizerAttributes(effectiveUser, "get")
		homeLogicalClusterName := h.getHomeLogicalClusterName(effectiveUser.GetName())

		homeClusterWorkspace, err := h.localInformers.getClusterWorkspace(homeLogicalClusterName)
		if err != nil && !kerrors.IsNotFound(err) {
			responsewriters.InternalError(rw, req, err)
			return
		}
		if homeClusterWorkspace != nil {
			// check for collision. Chance is super low hitting it by accident. But to protect against malicious users,
			// we check for collision and return 403.
			if info, _ := unmarshalOwner(homeClusterWorkspace); info == nil {
				responsewriters.Forbidden(ctx, getAttributes, rw, req, authorization.WorkspaceAcccessNotPermittedReason, homeWorkspaceCodecs)
				return
			} else if info.Username != effectiveUser.GetName() {
				klog.Warningf("Collision detected for user %q in home workspace %s owned by %q", effectiveUser.GetName(), homeLogicalClusterName, info.Username)
				responsewriters.Forbidden(ctx, getAttributes, rw, req, authorization.WorkspaceAcccessNotPermittedReason, homeWorkspaceCodecs)
				return
			}

			found, err := h.searchForHomeWorkspaceRBACResourcesInLocalInformers(homeLogicalClusterName)
			if err != nil {
				responsewriters.InternalError(rw, req, err)
				return
			}
			if found && homeClusterWorkspace.Status.Phase == tenancyv1alpha1.ClusterWorkspacePhaseReady {
				// We don't need to check any permission before returning the home workspace definition since,
				// once it has been created, a home workspace is owned by the user.
				homeWorkspace := &tenancyv1beta1.Workspace{}
				projection.ProjectClusterWorkspaceToWorkspace(homeClusterWorkspace, homeWorkspace)
				responsewriters.WriteObjectNegotiated(homeWorkspaceCodecs, negotiation.DefaultEndpointRestrictions, tenancyv1beta1.SchemeGroupVersion, rw, req, http.StatusOK, homeWorkspace)
				return
			}

			// fall through because either not ready or RBAC objects missing
		}

		// fall-through and let it be created
		lcluster.Name = homeLogicalClusterName
		workspaceType = HomeClusterWorkspaceType
	} else {
		if !lcluster.Name.HasPrefix(h.homePrefix) {
			// Not a home workspaces request
			h.apiHandler.ServeHTTP(rw, req)
			return
		}

		var needsAutomaticCreation bool
		needsAutomaticCreation, workspaceType = h.needsAutomaticCreation(lcluster.Name)
		klog.V(4).InfoS("Not a ~ request", "cluster", lcluster.Name, "needsAutoCreate", needsAutomaticCreation, "verb", requestInfo.Verb, "url", req.URL)
		if !needsAutomaticCreation {
			h.apiHandler.ServeHTTP(rw, req)
			return
		}

		foundLocally, retryAfterSeconds, err := h.searchForWorkspaceAndRBACInLocalInformers(lcluster.Name, workspaceType == HomeClusterWorkspaceType, effectiveUser.GetName())
		if err != nil {
			responsewriters.InternalError(rw, req, err)
			return
		}
		if foundLocally {
			klog.V(4).InfoS("Found home workspace", "cluster", lcluster.Name, "retryAfter", retryAfterSeconds)
			if retryAfterSeconds > 0 {
				// Return a 429 status asking the client to try again after the creationDelay
				rw.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSeconds))
				http.Error(rw, "Creating the home workspace", http.StatusTooManyRequests)
				return
			}
			h.apiHandler.ServeHTTP(rw, req)
			return
		}
	}

	// Home or bucket workspace not found in the local informer
	// Let's try to create it

	// But first check we have the right to do so.
	attributes := homeWorkspaceAuthorizerAttributes(effectiveUser, "create")
	if workspaceType == HomeClusterWorkspaceType && lcluster.Name != h.getHomeLogicalClusterName(effectiveUser.GetName()) {
		// If we're checking a home workspace or home bucket workspace, but not of the consistent user, let's refuse.
		utilruntime.HandleError(fmt.Errorf("failed to authorize user %q to create a home workspace %q: home workspace can only be created by the user of the home workspace", effectiveUser.GetName(), lcluster.Name))
		responsewriters.Forbidden(ctx, attributes, rw, req, authorization.WorkspaceAcccessNotPermittedReason, homeWorkspaceCodecs)
		return
	}

	// Test if the user has the right to create their Home workspace when it doesn't exist
	// => test the create verb on the clusterworkspaces/workspace subresource named ~ in the root workspace.
	if decision, reason, err := h.authz.Authorize(
		request.WithCluster(ctx, request.Cluster{Name: tenancyv1alpha1.RootCluster}),
		attributes,
	); err != nil {
		utilruntime.HandleError(fmt.Errorf("failed to authorize user %q to create a %s workspace %q: %w", effectiveUser.GetName(), workspaceType, lcluster.Name, err))
		responsewriters.Forbidden(ctx, attributes, rw, req, authorization.WorkspaceAcccessNotPermittedReason, homeWorkspaceCodecs)
		return
	} else if decision != authorizer.DecisionAllow {
		utilruntime.HandleError(fmt.Errorf("forbidden for user %q to create a %s workspace %q: %w", effectiveUser.GetName(), workspaceType, lcluster.Name, err))
		responsewriters.Forbidden(ctx, attributes, rw, req, reason, homeWorkspaceCodecs)
		return
	}

	klog.V(4).InfoS("ServeHTTP trying to create", "cluster", lcluster.Name, "workspaceType", workspaceType, "effectiveUser", effectiveUser.GetName())
	retryAfterSeconds, err := h.tryToCreate(ctx, effectiveUser, lcluster.Name, workspaceType)
	if err != nil {
		klog.Errorf("failed to create %s workspace %s for user %q: %w", workspaceType, lcluster.Name, effectiveUser.GetName(), err)
		responsewriters.ErrorNegotiated(err, errorCodecs, schema.GroupVersion{}, rw, req)
		return
	}

	// Return a 429 status asking the client to try again after the creationDelay
	klog.V(4).InfoS("ServeHTTP tryToCreate need to retry", "cluster", lcluster.Name, "workspaceType", workspaceType, "effectiveUser", effectiveUser.GetName(), "retryAfter", retryAfterSeconds)
	rw.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSeconds))
	http.Error(rw, "Creating the home workspace", http.StatusTooManyRequests)
}

// reHomeWorkspaceNameDisallowedChars is the regexp that defines what characters
// are disallowed in a home workspace name.
// Home workspace name is derived from the user name, with disallowed characters
// replaced.
var reHomeWorkspaceNameDisallowedChars = regexp.MustCompile("[^a-z0-9-]")

// getHomeLogicalClusterName returns the logicalcluster name of the home workspace for a given user
// The home workspace logical cluster ancestors are home bucket workspaces whose name is based
// on the user name sha1 hash.
func (h *homeWorkspaceHandler) getHomeLogicalClusterName(userName string) logicalcluster.Name {
	// bucketLevels <= 5
	// bucketSize <= 4
	bytes := sha1.Sum([]byte(userName))

	result := h.homePrefix
	for level := 0; level < h.bucketLevels; level++ {
		var bucketBytes = make([]byte, h.bucketSize)
		bucketBytesStart := level
		bucketCharInteger := binary.BigEndian.Uint32(bytes[bucketBytesStart : bucketBytesStart+4])
		for bucketCharIndex := 0; bucketCharIndex < h.bucketSize; bucketCharIndex++ {
			bucketChar := byte('a') + byte(bucketCharInteger%26)
			bucketBytes[bucketCharIndex] = bucketChar
			bucketCharInteger /= 26
		}
		result = result.Join(string(bucketBytes))
	}

	userName = reHomeWorkspaceNameDisallowedChars.ReplaceAllLiteralString(userName, "-")
	userName = strings.TrimLeftFunc(userName, func(r rune) bool {
		return r <= '9'
	})
	userName = strings.TrimRightFunc(userName, func(r rune) bool {
		return r == '-'
	})

	return result.Join(userName)
}

// needsAutomaticCreation deduces, from the logical cluster name,
// according to the expected home root and home bucket level number,
// whether the corresponding workspace has to be checked for automatic creation
// and what its workspace type will be.
func (h *homeWorkspaceHandler) needsAutomaticCreation(logicalClusterName logicalcluster.Name) (needsCheck bool, workspaceType tenancyv1alpha1.ClusterWorkspaceTypeName) {
	if !logicalClusterName.HasPrefix(h.homePrefix) {
		return false, ""
	}

	if logicalClusterName == h.homePrefix {
		return false, ""
	}

	levelsToHomePrefix := 0
	for lcluster := logicalClusterName; lcluster != h.homePrefix; lcluster, _ = lcluster.Split() {
		levelsToHomePrefix++
	}

	switch {
	case levelsToHomePrefix <= h.bucketLevels:
		return true, HomeBucketClusterWorkspaceType
	case levelsToHomePrefix == h.bucketLevels+1:
		return true, HomeClusterWorkspaceType
	default:
		return false, ""
	}
}

// searchForWorkspaceAndRBACInLocalInformers checks whether a workspace is known on the current shard
// in local informers.
// For home workspaces, we also check:
//   - if related RBAC resources are there,
// and if not answer to retry later.
func searchForWorkspaceAndRBACInLocalInformers(h *homeWorkspaceHandler, logicalClusterName logicalcluster.Name, isHome bool, userName string) (readyAndRBACAsExpected bool, retryAfterSeconds int, err error) {
	workspace, err := h.localInformers.getClusterWorkspace(logicalClusterName)
	if err != nil && !kerrors.IsNotFound(err) {
		return false, 0, err
	}
	if workspace == nil {
		return false, 0, nil
	}

	// Workspace has been found in local informer: check its status.
	if workspaceUnschedulable(workspace) {
		return false, 0, kerrors.NewForbidden(tenancyv1alpha1.SchemeGroupVersion.WithResource("clusterworkspaces").GroupResource(), logicalClusterName.String(), errors.New("unschedulable workspace cannot be accessed"))
	}

	if !isHome {
		// Waiting for home buckets to be ready is done in the `tryToCreate` function.
		return true, 0, nil
	}

	if workspace.Status.Phase != tenancyv1alpha1.ClusterWorkspacePhaseReady {
		// We have to wait for the workspace to be Ready before allowing actions in it,
		// but only for the the home workspaces.
		// Waiting for home buckets to be ready is done in the `tryToCreate` function.

		// TODO (david): For now we accept all requests in this workspace when it's initializing...
		// TODO (david): In the future, we might have a way to identify requests that are done through the
		// initializer virtual workspace
		// Or it might be unnecessary since other requests will always be blocked until initialization is finished.
		if workspace.Status.Phase != tenancyv1alpha1.ClusterWorkspacePhaseInitializing {
			return true, h.creationDelaySeconds, nil
		}
	}

	// For home workspaces, also wait for related RBAC resources to be setup.
	if rbacResourcesFound, err := h.searchForHomeWorkspaceRBACResourcesInLocalInformers(logicalClusterName); err != nil {
		return false, 0, err
	} else if !rbacResourcesFound {
		// Retry sooner than the creation delay, because it's probably a question of
		// the local informer cache not being up-to-date, or the request having been sent
		// to the wrong shard.
		// Retrying quicly should be sufficient to fix it.
		return true, 1, nil
	}
	return true, 0, nil
}

// tryToCreate tries to create, with an external client, the home or home bucket workspace
// corresponding to a given logical cluster name.
// Of course, it can be that it has been created in the meantime (either concurrently on the current shard or
// on another shard). We don't error in this case, but ask for a retry.
// If it cannot be created because the parent home bucket doesn't exist, create the parent first and
// retry the creation of the current workspace later.
// When creating a Home workspace, we also create the related RBAC resources.
func tryToCreate(h *homeWorkspaceHandler, ctx context.Context, user kuser.Info, logicalClusterName logicalcluster.Name, workspaceType tenancyv1alpha1.ClusterWorkspaceTypeName) (retryAfterSeconds int, createError error) {
	// Try to create it in the parent workspace
	parent, name := logicalClusterName.Split()
	ws := &tenancyv1alpha1.ClusterWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
			Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{Path: tenancyv1alpha1.RootCluster.String(), Name: workspaceType},
		},
	}
	if workspaceType == HomeClusterWorkspaceType {
		ownerRaw, err := clusterworkspaceadmission.ClusterWorkspaceOwnerAnnotationValue(user)
		if err != nil {
			return 0, fmt.Errorf("failed to create %s annotation value: %w", tenancyv1alpha1.ExperimentalClusterWorkspaceOwnerAnnotationKey, err)
		}
		ws.Annotations = map[string]string{
			tenancyv1alpha1.ExperimentalClusterWorkspaceOwnerAnnotationKey: ownerRaw,
		}
	}

	klog.V(4).InfoS("tryToCreate: creating workspace", "parent", parent.String(), "workspace", ws.Name)
	err := h.kcp.createClusterWorkspace(ctx, parent, ws)
	klog.V(4).InfoS("tryToCreate: creating workspace result", "parent", parent.String(), "workspace", ws.Name, "err", err)
	if err == nil || kerrors.IsAlreadyExists(err) {
		if workspaceType == HomeClusterWorkspaceType {
			if kerrors.IsAlreadyExists(err) {
				cw, err := h.kcp.getClusterWorkspace(ctx, parent, name)
				if err != nil {
					return 0, err
				}

				if info, _ := unmarshalOwner(cw); info == nil {
					return 0, kerrors.NewForbidden(tenancyv1alpha1.SchemeGroupVersion.WithResource("clusterworkspaces").GroupResource(), cw.Name, errors.New(authorization.WorkspaceAcccessNotPermittedReason))
				} else if info.Username != user.GetName() {
					klog.Warningf("Collision detected for user %q in home workspace %s owned by %q", user.GetName(), logicalClusterName, info.Username)
					return 0, kerrors.NewForbidden(tenancyv1alpha1.SchemeGroupVersion.WithResource("clusterworkspaces").GroupResource(), cw.Name, errors.New(authorization.WorkspaceAcccessNotPermittedReason))
				}
			}

			klog.V(4).InfoS("tryToCreate: creating rbac resources", "cluster", logicalClusterName)
			if err := h.createHomeWorkspaceRBACResources(ctx, user.GetName(), logicalClusterName); err != nil {
				klog.V(4).InfoS("tryToCreate: error creating rbac resources", "cluster", logicalClusterName, "err", err)
				return 0, err
			}
		}
		// Retry sooner than the creation delay, because it's probably a question of
		// letting the local informer cache being updated.
		// Retrying quickly (after 1 second) should be sufficient to it.
		klog.V(4).InfoS("tryToCreate: returning retry", "cluster", logicalClusterName, "retryAfter", 1)
		return 1, nil
	}

	if retryAfterCreateSeconds, shouldRetry := kerrors.SuggestsClientDelay(err); shouldRetry {
		klog.V(4).InfoS("tryToCreate: client suggests delay", "cluster", logicalClusterName, "retryAfter", retryAfterCreateSeconds)
		return retryAfterCreateSeconds, nil
	}

	if !kerrors.IsForbidden(err) && !kerrors.IsNotFound(err) {
		klog.V(4).InfoS("tryToCreate: returning error", "cluster", logicalClusterName, "err", err)
		return 0, err
	}

	// The error returned by the creation attempt is either Forbidden or NotFound (due to APIBindings not being ready).
	// We have to detect whether it is because:
	//
	// - the parent workspace doesn't exist (in which case we'd try to create it),
	// - the parent workspace isn't ready but will be soon (in which case we'd return a retry response)
	// - of another reason, in which case we'd return the error

	if parent == h.homePrefix {
		// If the parent itself is the home root, which is created by default => return the error.
		return 0, err
	}

	grandParent, parentName := parent.Split()
	// Check in the grand parentWorkspace if the parentWorkspace itself exists
	parentWorkspace, _ := h.kcp.getClusterWorkspace(ctx, grandParent, parentName)

	if parentWorkspace == nil {
		// The parent simply probably does not exist => try to create it first
		_, parentWorkspaceType := h.needsAutomaticCreation(parent)
		klog.V(4).InfoS("tryToCreate: trying to create parent", "cluster", logicalClusterName, "parent", parent, "parentType", parentWorkspaceType)
		return h.tryToCreate(ctx, user, parent, parentWorkspaceType)
	}

	// The parent exists: check its status
	if parentWorkspace.Status.Phase == tenancyv1alpha1.ClusterWorkspacePhaseReady {
		// if we received 403 but the parent exists and is ready,
		// there's no reason to wait more => return the error.
		klog.V(4).InfoS("tryToCreate: parent is ready, returning 0 retry and possible error", "cluster", logicalClusterName, "parent", parent, "err", err)
		return 0, err
	}

	if workspaceUnschedulable(parentWorkspace) {
		klog.V(4).InfoS("tryToCreate: parent is not ready and unschedulable, returning 0 retry and possible error", "cluster", logicalClusterName, "parent", parent, "err", err)
		// if we received 403 since the parent exists but cannot be scheduled,
		// there's no reason to wait more => return the error.
		return 0, err
	}

	// We have to wait for the parent workspace to be Ready before trying to create the child workspace
	klog.V(4).InfoS("tryToCreate: parent is not ready, returning retry", "cluster", logicalClusterName, "parent", parent, "retryAfter", h.creationDelaySeconds)
	return h.creationDelaySeconds, nil
}

// searchForHomeWorkspaceRBACResourcesInLocalInformers searches for the expected RBAC resources associated to a Home workspace
// in the local informers.
func searchForHomeWorkspaceRBACResourcesInLocalInformers(h *homeWorkspaceHandler, logicalClusterName logicalcluster.Name) (found bool, err error) {
	parent, workspaceName := logicalClusterName.Split()

	if _, err := h.localInformers.getClusterRole(parent, homeOwnerClusterRolePrefix+workspaceName); kerrors.IsNotFound(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if _, err := h.localInformers.getClusterRoleBinding(parent, homeOwnerClusterRolePrefix+workspaceName); kerrors.IsNotFound(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}

	return true, nil
}

// createHomeWorkspaceRBACResources uses an external client to create the RBAC resources related to a Home workspace
// Note: it only create a cluster role or cluster role binding if it doesn't exist.
// For now it doesn't try to detect and update (or recreate) RBAC resources that might be obsolete.
func createHomeWorkspaceRBACResources(h *homeWorkspaceHandler, ctx context.Context, userName string, homeWorkspace logicalcluster.Name) error {
	parent, name := homeWorkspace.Split()
	if err := h.kcp.createClusterRole(ctx, parent, &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: homeOwnerClusterRolePrefix + name,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups:     []string{tenancyv1beta1.SchemeGroupVersion.Group},
				Resources:     []string{"clusterworkspaces/content"},
				Verbs:         []string{"access", "admin"},
				ResourceNames: []string{name},
			},
		},
	}); err != nil && !kerrors.IsAlreadyExists(err) {
		return err
	}

	if err := h.kcp.createClusterRoleBinding(ctx, parent, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: homeOwnerClusterRolePrefix + name,
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			APIGroup: "rbac.authorization.k8s.io",
			Name:     homeOwnerClusterRolePrefix + name,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "User",
				Name:      userName,
				Namespace: "",
			},
		},
	}); err != nil && !kerrors.IsAlreadyExists(err) {
		return err
	}

	return nil
}

func homeWorkspaceAuthorizerAttributes(user kuser.Info, verb string) authorizer.Attributes {
	return authorizer.AttributesRecord{
		User:            user,
		Verb:            verb,
		APIGroup:        tenancyv1alpha1.SchemeGroupVersion.Group,
		Resource:        "clusterworkspaces",
		Subresource:     "workspace",
		Name:            "~",
		ResourceRequest: true,
	}
}

func workspaceUnschedulable(workspace *tenancyv1alpha1.ClusterWorkspace) bool {
	if notscheduled, reason := conditions.IsFalse(workspace, tenancyv1alpha1.WorkspaceScheduled),
		conditions.GetReason(workspace, tenancyv1alpha1.WorkspaceScheduled); notscheduled &&
		(reason == tenancyv1alpha1.WorkspaceReasonUnschedulable ||
			reason == tenancyv1alpha1.WorkspaceReasonReasonUnknown ||
			reason == tenancyv1alpha1.WorkspaceReasonUnreschedulable) {
		return true
	}
	return false
}

func unmarshalOwner(cw *tenancyv1alpha1.ClusterWorkspace) (*authenticationv1.UserInfo, error) {
	raw, found := cw.Annotations[tenancyv1alpha1.ExperimentalClusterWorkspaceOwnerAnnotationKey]
	if !found {
		return nil, nil
	}
	var info authenticationv1.UserInfo
	err := json.Unmarshal([]byte(raw), &info)
	if err != nil {
		return nil, err
	}
	return &info, err
}
