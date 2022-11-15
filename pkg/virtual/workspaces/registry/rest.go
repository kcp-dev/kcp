/*
Copyright 2021 The KCP Authors.

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

package registry

import (
	"context"
	"fmt"

	"github.com/kcp-dev/logicalcluster/v2"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apimachinery/pkg/watch"
	apirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/printers"
	printerstorage "k8s.io/kubernetes/pkg/printers/storage"

	clusterworkspaceadmission "github.com/kcp-dev/kcp/pkg/admission/clusterworkspace"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/projection"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	workspaceprinters "github.com/kcp-dev/kcp/pkg/virtual/workspaces/printers"
)

type WorkspacesScopeKeyType string

const (
	ClusterKey WorkspacesScopeKeyType = "VirtualWorkspaceWorkspacesOrg"
)

type REST struct {
	kcpClusterClient kcpclientset.ClusterInterface
	delegatedAuthz   delegated.DelegatedAuthorizerFactory
	rest.TableConvertor
}

var _ rest.Getter = &REST{}
var _ rest.Lister = &REST{}
var _ rest.Watcher = &REST{}
var _ rest.Scoper = &REST{}
var _ rest.Creater = &REST{}
var _ rest.GracefulDeleter = &REST{}

// NewREST returns a RESTStorage object that will work against ClusterWorkspace resources in
// org workspaces, projecting them to the Workspace type.
func NewREST(
	kcpClusterClient kcpclientset.ClusterInterface,
) *REST {
	mainRest := &REST{
		kcpClusterClient: kcpClusterClient,
		delegatedAuthz:   delegated.NewDelegatedAuthorizer,

		TableConvertor: printerstorage.TableConvertor{TableGenerator: printers.NewTableGenerator().With(workspaceprinters.AddWorkspacePrintHandlers)},
	}

	return mainRest
}

// New returns a new ClusterWorkspace
func (s *REST) New() runtime.Object {
	return &tenancyv1beta1.Workspace{}
}

// Destroy implements rest.Storage
func (s *REST) Destroy() {
	// Do nothing
}

// NewList returns a new ClusterWorkspaceList
func (*REST) NewList() runtime.Object {
	return &tenancyv1beta1.WorkspaceList{}
}

func (s *REST) NamespaceScoped() bool {
	return false
}

// List retrieves a list of Workspaces that match label.
func (s *REST) List(ctx context.Context, options *metainternal.ListOptions) (runtime.Object, error) {
	orgClusterName := ctx.Value(ClusterKey).(logicalcluster.Name)

	clusterWorkspaceList := &tenancyv1alpha1.ClusterWorkspaceList{}
	v1Opts := metav1.ListOptions{}
	if err := metainternal.Convert_internalversion_ListOptions_To_v1_ListOptions(options, &v1Opts, nil); err != nil {
		return nil, err
	}
	var err error
	clusterWorkspaceList, err = s.kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().List(ctx, v1Opts)
	if err != nil {
		return nil, err
	}

	workspaceList := &tenancyv1beta1.WorkspaceList{
		ListMeta: clusterWorkspaceList.ListMeta,
		Items:    make([]tenancyv1beta1.Workspace, len(clusterWorkspaceList.Items)),
	}

	for i, cws := range clusterWorkspaceList.Items {
		projection.ProjectClusterWorkspaceToWorkspace(&cws, &workspaceList.Items[i])
	}

	return workspaceList, nil
}

func (s *REST) Watch(ctx context.Context, options *metainternal.ListOptions) (watch.Interface, error) {
	orgClusterName := ctx.Value(ClusterKey).(logicalcluster.Name)

	v1Opts := metav1.ListOptions{}
	if err := metainternal.Convert_internalversion_ListOptions_To_v1_ListOptions(options, &v1Opts, nil); err != nil {
		return nil, err
	}
	w, err := s.kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Watch(ctx, v1Opts)
	if err != nil {
		return nil, err
	}
	return withProjection{delegate: w, ch: make(chan watch.Event)}, nil
}

// Get retrieves a Workspace by name
func (s *REST) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	opts := metav1.GetOptions{}
	if options != nil {
		opts = *options
	}
	orgClusterName := ctx.Value(ClusterKey).(logicalcluster.Name)
	cws, err := s.kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, name, opts)
	if err != nil {
		return nil, err
	}

	var ws tenancyv1beta1.Workspace
	projection.ProjectClusterWorkspaceToWorkspace(cws, &ws)
	return &ws, nil
}

// Create creates a new workspace.
// The corresponding ClusterWorkspace resource is created in the underlying KCP server.
func (s *REST) Create(ctx context.Context, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
	workspace, isWorkspace := obj.(*tenancyv1beta1.Workspace)
	if !isWorkspace {
		return nil, kerrors.NewInvalid(tenancyv1beta1.SchemeGroupVersion.WithKind("Workspace").GroupKind(), obj.GetObjectKind().GroupVersionKind().String(), []*field.Error{})
	}

	userInfo, ok := apirequest.UserFrom(ctx)
	if !ok {
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), "", fmt.Errorf("unable to create a workspace without a user on the context"))
	}

	orgClusterName := ctx.Value(ClusterKey).(logicalcluster.Name)

	// Create the workspace object itself
	clusterWorkspace := &tenancyv1alpha1.ClusterWorkspace{
		ObjectMeta: workspace.ObjectMeta,
		Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
			Type: workspace.Spec.Type,
		},
	}

	ownerRaw, err := clusterworkspaceadmission.ClusterWorkspaceOwnerAnnotationValue(userInfo)
	if err != nil {
		return nil, fmt.Errorf("error constructing workspace owner annotation from user info: %w", err)
	}

	if clusterWorkspace.Annotations == nil {
		clusterWorkspace.Annotations = make(map[string]string)
	}

	clusterWorkspace.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey] = ownerRaw

	createdClusterWorkspace, err := s.kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, clusterWorkspace, metav1.CreateOptions{})
	if kerrors.IsAlreadyExists(err) {
		return nil, kerrors.NewAlreadyExists(tenancyv1beta1.Resource("workspaces"), workspace.Name)
	}
	if err != nil {
		return nil, err
	}
	var createdWorkspace tenancyv1beta1.Workspace
	projection.ProjectClusterWorkspaceToWorkspace(createdClusterWorkspace, &createdWorkspace)

	return &createdWorkspace, nil
}

var _ = rest.GracefulDeleter(&REST{})

func (s *REST) Delete(ctx context.Context, name string, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions) (runtime.Object, bool, error) {
	orgClusterName := ctx.Value(ClusterKey).(logicalcluster.Name)
	logger := klog.FromContext(ctx).WithValues("parent", orgClusterName, "name", name)
	ctx = klog.NewContext(ctx, logger)

	err := s.kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Delete(ctx, name, *options)
	if kerrors.IsNotFound(err) {
		err = kerrors.NewNotFound(tenancyv1beta1.Resource("workspaces"), name)
	}

	return nil, false, err
}

type withProjection struct {
	delegate watch.Interface
	ch       chan watch.Event
}

func (w withProjection) ResultChan() <-chan watch.Event {
	ch := w.delegate.ResultChan()

	go func() {
		defer close(w.ch)
		for ev := range ch {
			if ev.Object == nil {
				w.ch <- ev
				continue
			}
			if cws, ok := ev.Object.(*tenancyv1alpha1.ClusterWorkspace); ok {
				ws := &tenancyv1beta1.Workspace{}
				projection.ProjectClusterWorkspaceToWorkspace(cws, ws)
				ev.Object = ws
			}
			w.ch <- ev
		}
	}()

	return w.ch
}

func (w withProjection) Stop() {
	w.delegate.Stop()
}
