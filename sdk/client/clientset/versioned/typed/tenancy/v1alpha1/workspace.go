/*
Copyright The KCP Authors.

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

// Code generated by client-gen. DO NOT EDIT.

package v1alpha1

import (
	context "context"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	gentype "k8s.io/client-go/gentype"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	applyconfigurationtenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/client/applyconfiguration/tenancy/v1alpha1"
	scheme "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/scheme"
)

// WorkspacesGetter has a method to return a WorkspaceInterface.
// A group's client should implement this interface.
type WorkspacesGetter interface {
	Workspaces() WorkspaceInterface
}

// WorkspaceInterface has methods to work with Workspace resources.
type WorkspaceInterface interface {
	Create(ctx context.Context, workspace *tenancyv1alpha1.Workspace, opts v1.CreateOptions) (*tenancyv1alpha1.Workspace, error)
	Update(ctx context.Context, workspace *tenancyv1alpha1.Workspace, opts v1.UpdateOptions) (*tenancyv1alpha1.Workspace, error)
	// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().
	UpdateStatus(ctx context.Context, workspace *tenancyv1alpha1.Workspace, opts v1.UpdateOptions) (*tenancyv1alpha1.Workspace, error)
	Delete(ctx context.Context, name string, opts v1.DeleteOptions) error
	DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error
	Get(ctx context.Context, name string, opts v1.GetOptions) (*tenancyv1alpha1.Workspace, error)
	List(ctx context.Context, opts v1.ListOptions) (*tenancyv1alpha1.WorkspaceList, error)
	Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error)
	Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *tenancyv1alpha1.Workspace, err error)
	Apply(ctx context.Context, workspace *applyconfigurationtenancyv1alpha1.WorkspaceApplyConfiguration, opts v1.ApplyOptions) (result *tenancyv1alpha1.Workspace, err error)
	// Add a +genclient:noStatus comment above the type to avoid generating ApplyStatus().
	ApplyStatus(ctx context.Context, workspace *applyconfigurationtenancyv1alpha1.WorkspaceApplyConfiguration, opts v1.ApplyOptions) (result *tenancyv1alpha1.Workspace, err error)
	WorkspaceExpansion
}

// workspaces implements WorkspaceInterface
type workspaces struct {
	*gentype.ClientWithListAndApply[*tenancyv1alpha1.Workspace, *tenancyv1alpha1.WorkspaceList, *applyconfigurationtenancyv1alpha1.WorkspaceApplyConfiguration]
}

// newWorkspaces returns a Workspaces
func newWorkspaces(c *TenancyV1alpha1Client) *workspaces {
	return &workspaces{
		gentype.NewClientWithListAndApply[*tenancyv1alpha1.Workspace, *tenancyv1alpha1.WorkspaceList, *applyconfigurationtenancyv1alpha1.WorkspaceApplyConfiguration](
			"workspaces",
			c.RESTClient(),
			scheme.ParameterCodec,
			"",
			func() *tenancyv1alpha1.Workspace { return &tenancyv1alpha1.Workspace{} },
			func() *tenancyv1alpha1.WorkspaceList { return &tenancyv1alpha1.WorkspaceList{} },
		),
	}
}
