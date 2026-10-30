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
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	"github.com/kcp-dev/sdk/apis/core"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
)

func createAttr(path, name string) admission.Attributes {
	slice := &cachev1alpha1.ClusterCachedResourceEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "slice"},
		Spec: cachev1alpha1.ClusterCachedResourceEndpointSliceSpec{
			ClusterCachedResource: cachev1alpha1.ClusterCachedResourceReference{Path: path, Name: name},
		},
	}
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(slice),
		nil,
		cachev1alpha1.Kind("ClusterCachedResourceEndpointSlice").WithVersion("v1alpha1"),
		"",
		slice.Name,
		cachev1alpha1.Resource("clustercachedresourceendpointslices").WithVersion("v1alpha1"),
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{Name: "user-2"},
	)
}

type fakeAuthorizer struct {
	decision authorizer.Decision
	err      error
	attr     authorizer.Attributes
}

func (a *fakeAuthorizer) Authorize(_ context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	a.attr = attr
	return a.decision, "reason", a.err
}

func TestValidate(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		attr        admission.Attributes
		decision    authorizer.Decision
		authzErr    error
		wantErr     string
		wantCluster logicalcluster.Name
	}{
		"missing name is rejected": {
			attr:    createAttr("", ""),
			wantErr: "spec.clusterCachedResource.name: Required value",
		},
		"local reference is authorized in the local workspace": {
			attr:        createAttr("", "sheriffs.wildwest.dev"),
			decision:    authorizer.DecisionAllow,
			wantCluster: "consumer",
		},
		"local reference denied": {
			attr:     createAttr("", "sheriffs.wildwest.dev"),
			decision: authorizer.DecisionDeny,
			wantErr:  "no permission to bind to ClusterCachedResource sheriffs.wildwest.dev",
		},
		"foreign reference is authorized in the foreign workspace": {
			attr:        createAttr("root:org:provider", "sheriffs.wildwest.dev"),
			decision:    authorizer.DecisionAllow,
			wantCluster: "provider",
		},
		"foreign reference denied": {
			attr:     createAttr("root:org:provider", "sheriffs.wildwest.dev"),
			decision: authorizer.DecisionDeny,
			wantErr:  "no permission to bind to ClusterCachedResource root:org:provider:sheriffs.wildwest.dev",
		},
		"foreign reference with no opinion is denied": {
			attr:     createAttr("root:org:provider", "sheriffs.wildwest.dev"),
			decision: authorizer.DecisionNoOpinion,
			wantErr:  "no permission to bind to ClusterCachedResource root:org:provider:sheriffs.wildwest.dev",
		},
		"authorizer error is denied": {
			attr:     createAttr("root:org:provider", "sheriffs.wildwest.dev"),
			authzErr: errors.New("boom"),
			wantErr:  "no permission to bind to ClusterCachedResource root:org:provider:sheriffs.wildwest.dev",
		},
		"unknown foreign reference is denied with the same error even if the authorizer would allow": {
			attr:     createAttr("root:org:provider", "does-not-exist"),
			decision: authorizer.DecisionAllow,
			wantErr:  "no permission to bind to ClusterCachedResource root:org:provider:does-not-exist",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := genericapirequest.WithCluster(context.Background(), genericapirequest.Cluster{Name: "consumer"})
			authz := &fakeAuthorizer{decision: tc.decision, err: tc.authzErr}
			var sarCluster logicalcluster.Name
			plugin := Admission{
				Handler: admission.NewHandler(admission.Create),
				createAuthorizer: func(clusterName logicalcluster.Name, _ kcpkubernetesclientset.ClusterInterface, _ delegated.Options) (authorizer.Authorizer, error) {
					sarCluster = clusterName
					return authz, nil
				},
				getClusterCachedResource: func(path logicalcluster.Path, name string) (*cachev1alpha1.ClusterCachedResource, error) {
					if path.String() == "root:org:provider" && name == "sheriffs.wildwest.dev" {
						return &cachev1alpha1.ClusterCachedResource{ObjectMeta: metav1.ObjectMeta{
							Name: name,
							Annotations: map[string]string{
								logicalcluster.AnnotationKey:         "provider",
								core.LogicalClusterPathAnnotationKey: path.String(),
							},
						}}, nil
					}
					return nil, apierrors.NewNotFound(cachev1alpha1.Resource("clustercachedresources"), name)
				},
			}

			err := plugin.Validate(ctx, tc.attr, nil)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantCluster, sarCluster, "SubjectAccessReview must run in the ClusterCachedResource's workspace")
			require.Equal(t, "bind", authz.attr.GetVerb())
			require.Equal(t, "clustercachedresources", authz.attr.GetResource())
			require.Equal(t, cachev1alpha1.SchemeGroupVersion.Group, authz.attr.GetAPIGroup())
			require.Equal(t, "sheriffs.wildwest.dev", authz.attr.GetName())
			require.Equal(t, "user-2", authz.attr.GetUser().GetName())
		})
	}
}
