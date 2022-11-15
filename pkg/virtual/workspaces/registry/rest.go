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

// NewREST returns a RESTStorage object that will work against Workspace resources,
// projecting them to the Workspace type.
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
	clusterName := ctx.Value(ClusterKey).(logicalcluster.Name)

	ws := &tenancyv1beta1.WorkspaceList{}
	v1Opts := metav1.ListOptions{}
	if err := metainternal.Convert_internalversion_ListOptions_To_v1_ListOptions(options, &v1Opts, nil); err != nil {
		return nil, err
	}
	var err error
	ws, err = s.kcpClusterClient.Cluster(clusterName).TenancyV1beta1().Workspaces().List(ctx, v1Opts)
	if err != nil {
		return nil, err
	}

	cws := &tenancyv1alpha1.ClusterWorkspaceList{
		ListMeta: ws.ListMeta,
		Items:    make([]tenancyv1alpha1.ClusterWorkspace, len(ws.Items)),
	}

	for i, w := range ws.Items {
		projection.ProjectWorkspaceToClusterWorkspace(&w, &cws.Items[i])
	}

	return cws, nil
}

func (s *REST) Watch(ctx context.Context, options *metainternal.ListOptions) (watch.Interface, error) {
	clusterName := ctx.Value(ClusterKey).(logicalcluster.Name)

	v1Opts := metav1.ListOptions{}
	if err := metainternal.Convert_internalversion_ListOptions_To_v1_ListOptions(options, &v1Opts, nil); err != nil {
		return nil, err
	}
	w, err := s.kcpClusterClient.Cluster(clusterName).TenancyV1beta1().Workspaces().Watch(ctx, v1Opts)
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
	clusterName := ctx.Value(ClusterKey).(logicalcluster.Name)
	ws, err := s.kcpClusterClient.Cluster(clusterName).TenancyV1beta1().Workspaces().Get(ctx, name, opts)
	if err != nil {
		return nil, err
	}

	var cws tenancyv1alpha1.ClusterWorkspace
	projection.ProjectWorkspaceToClusterWorkspace(ws, &cws)
	return &cws, nil
}

// Create creates a new workspace.
func (s *REST) Create(ctx context.Context, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
	cws, isWorkspace := obj.(*tenancyv1alpha1.ClusterWorkspace)
	if !isWorkspace {
		return nil, kerrors.NewInvalid(tenancyv1alpha1.SchemeGroupVersion.WithKind("ClusterWorkspace").GroupKind(), obj.GetObjectKind().GroupVersionKind().String(), []*field.Error{})
	}

	userInfo, ok := apirequest.UserFrom(ctx)
	if !ok {
		return nil, kerrors.NewForbidden(tenancyv1alpha1.Resource("clusterworkspaces"), "", fmt.Errorf("unable to create a clustersworkspace without a user on the context"))
	}

	clusterName := ctx.Value(ClusterKey).(logicalcluster.Name)
	ws := &tenancyv1beta1.Workspace{
		ObjectMeta: cws.ObjectMeta,
		Spec: tenancyv1beta1.WorkspaceSpec{
			Type: cws.Spec.Type,
		},
	}

	ownerRaw, err := clusterworkspaceadmission.ClusterWorkspaceOwnerAnnotationValue(userInfo)
	if err != nil {
		return nil, fmt.Errorf("error constructing workspace owner annotation from user info: %w", err)
	}

	if ws.Annotations == nil {
		ws.Annotations = make(map[string]string)
	}
	ws.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey] = ownerRaw

	if cws.Spec.Shard != nil {
		ws.Spec.Location = &tenancyv1beta1.WorkspaceLocation{
			Selector: cws.Spec.Shard.Selector,
		}
		if cws.Spec.Shard.Name != "" {
			if ws.Spec.Location.Selector == nil {
				ws.Spec.Location.Selector = &metav1.LabelSelector{}
			}
			if ws.Spec.Location.Selector.MatchLabels == nil {
				ws.Spec.Location.Selector.MatchLabels = make(map[string]string)
			}
			ws.Spec.Location.Selector.MatchLabels["name"] = cws.Spec.Shard.Name
		}
	}

	createdWS, err := s.kcpClusterClient.Cluster(clusterName).TenancyV1beta1().Workspaces().Create(ctx, ws, metav1.CreateOptions{})
	if kerrors.IsAlreadyExists(err) {
		return nil, kerrors.NewAlreadyExists(tenancyv1alpha1.Resource("clusterworkspaces"), cws.Name)
	}
	if err != nil {
		return nil, err
	}
	var createdCWS tenancyv1alpha1.ClusterWorkspace
	projection.ProjectWorkspaceToClusterWorkspace(createdWS, &createdCWS)

	return &createdCWS, nil
}

func (s *REST) Delete(ctx context.Context, name string, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions) (runtime.Object, bool, error) {
	clusterName := ctx.Value(ClusterKey).(logicalcluster.Name)
	logger := klog.FromContext(ctx).WithValues("cluster", clusterName, "name", name)
	ctx = klog.NewContext(ctx, logger)

	err := s.kcpClusterClient.Cluster(clusterName).TenancyV1beta1().Workspaces().Delete(ctx, name, *options)
	if kerrors.IsNotFound(err) {
		err = kerrors.NewNotFound(tenancyv1beta1.Resource("clusterworkspaces"), name)
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
			if ws, ok := ev.Object.(*tenancyv1beta1.Workspace); ok {
				cws := &tenancyv1alpha1.ClusterWorkspace{}
				projection.ProjectWorkspaceToClusterWorkspace(ws, cws)
				ev.Object = cws
			}
			w.ch <- ev
		}
	}()

	return w.ch
}

func (w withProjection) Stop() {
	w.delegate.Stop()
}
