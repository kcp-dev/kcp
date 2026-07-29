/*
Copyright 2026 The kcp Authors.

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

// Package scar implements the SelfClusterAccessReview API of the
// Access Virtual Workspace.
package scar

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"

	accessv1alpha1 "github.com/kcp-dev/kcp/contrib/access-vw/pkg/apis/access/v1alpha1"
	"github.com/kcp-dev/kcp/contrib/access-vw/pkg/graph"
)

type REST struct {
	graph *graph.Graph
}

var (
	_ rest.Storage              = &REST{}
	_ rest.Creater              = &REST{}
	_ rest.Scoper               = &REST{}
	_ rest.SingularNameProvider = &REST{}
)

// NewREST returns the REST storage backed by the given access graph.
func NewREST(g *graph.Graph) *REST {
	return &REST{graph: g}
}

// New returns an empty SelfClusterAccessReview.
func (r *REST) New() runtime.Object {
	return &accessv1alpha1.SelfClusterAccessReview{}
}

// Destroy is a no-op; the storage holds no resources that need cleanup.
func (r *REST) Destroy() {}

// NamespaceScoped reports that the resource is cluster-scoped.
func (r *REST) NamespaceScoped() bool {
	return false
}

// GetSingularName returns the singular resource name.
func (r *REST) GetSingularName() string {
	return "selfclusteraccessreview"
}

// Create answers the self-review, it resolves the caller from the
// request context and fills Status.Clusters from the access graph.
func (r *REST) Create(ctx context.Context, obj runtime.Object, _ rest.ValidateObjectFunc, _ *metav1.CreateOptions) (runtime.Object, error) {
	review, ok := obj.(*accessv1alpha1.SelfClusterAccessReview)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("not a SelfClusterAccessReview: %T", obj))
	}

	if !r.graph.Ready() {
		return nil, apierrors.NewServiceUnavailable("access graph is not ready; try again shortly")
	}

	user, ok := genericapirequest.UserFrom(ctx)
	if !ok {
		return nil, apierrors.NewUnauthorized("no user present in request context")
	}

	clusters := r.graph.ClustersFor(user.GetName(), user.GetGroups())
	out := review.DeepCopy()
	out.Status = accessv1alpha1.SelfClusterAccessReviewStatus{
		Clusters: toAccessEndpointSlices(clusters),
	}

	return out, nil
}

func toAccessEndpointSlices(in []graph.AccessEndpointSlice) []accessv1alpha1.AccessEndpointSlice {
	if in == nil {
		return nil
	}

	out := make([]accessv1alpha1.AccessEndpointSlice, len(in))
	for i, s := range in {
		out[i] = accessv1alpha1.AccessEndpointSlice{
			ClusterName: s.ClusterName,
			Endpoint:    s.Endpoint,
		}
	}

	return out
}
