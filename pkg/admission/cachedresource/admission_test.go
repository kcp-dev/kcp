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

package cachedresource

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	"github.com/kcp-dev/kcp/pkg/reconciler/dynamicrestmapper"
)

func createAttr(cachedResource *cachev1alpha1.CachedResource) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(cachedResource),
		nil,
		cachev1alpha1.Kind("CachedResource").WithVersion("v1alpha1"),
		"",
		cachedResource.Name,
		cachev1alpha1.Resource("cachedresources").WithVersion("v1alpha1"),
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func updateAttr(cachedResource *cachev1alpha1.CachedResource) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(cachedResource),
		nil,
		cachev1alpha1.Kind("CachedResource").WithVersion("v1alpha1"),
		"",
		cachedResource.Name,
		cachev1alpha1.Resource("cachedresources").WithVersion("v1alpha1"),
		"",
		admission.Update,
		&metav1.UpdateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func createCachedResource(name string, gvr schema.GroupVersionResource) *cachev1alpha1.CachedResource {
	return &cachev1alpha1.CachedResource{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: cachev1alpha1.CachedResourceSpec{
			GroupVersionResource: cachev1alpha1.GroupVersionResource{
				Group:    gvr.Group,
				Version:  gvr.Version,
				Resource: gvr.Resource,
			},
		},
	}
}

func TestAdmission(t *testing.T) {
	cases := map[string]struct {
		attr    admission.Attributes
		index   map[logicalcluster.Name]map[schema.GroupVersionResource][]*cachev1alpha1.CachedResource
		cluster logicalcluster.Name
		gvr     schema.GroupVersionResource
		wantErr error
	}{
		"Empty": {
			attr: createAttr(createCachedResource("wohoo", schema.GroupVersionResource{
				Group:    "example.org",
				Version:  "v1",
				Resource: "objects",
			})),
			index:   map[logicalcluster.Name]map[schema.GroupVersionResource][]*cachev1alpha1.CachedResource{},
			cluster: logicalcluster.Name("cluster-1"),
		},
		"New": {
			attr: createAttr(createCachedResource("wohoo", schema.GroupVersionResource{
				Group:    "example.org",
				Version:  "v1",
				Resource: "objects",
			})),
			index: map[logicalcluster.Name]map[schema.GroupVersionResource][]*cachev1alpha1.CachedResource{
				"cluster-2": {
					schema.GroupVersionResource{
						Group:    "example.org",
						Version:  "v1",
						Resource: "objects",
					}: []*cachev1alpha1.CachedResource{
						createCachedResource("cluster-2-example-org-v1-objects", schema.GroupVersionResource{
							Group:    "example.org",
							Version:  "v1",
							Resource: "objects",
						}),
					},
				},
			},
			cluster: logicalcluster.Name("cluster-1"),
		},
		"AlreadyExists": {
			attr: createAttr(createCachedResource("wohoo", schema.GroupVersionResource{
				Group:    "example.org",
				Version:  "v1",
				Resource: "objects",
			})),
			index: map[logicalcluster.Name]map[schema.GroupVersionResource][]*cachev1alpha1.CachedResource{
				"cluster-2": {
					schema.GroupVersionResource{
						Group:    "example.org",
						Version:  "v1",
						Resource: "objects",
					}: []*cachev1alpha1.CachedResource{
						createCachedResource("cluster-2-example-org-v1-objects", schema.GroupVersionResource{
							Group:    "example.org",
							Version:  "v1",
							Resource: "objects",
						}),
					},
				},
			},
			wantErr: admission.NewForbidden(createAttr(createCachedResource("wohoo", schema.GroupVersionResource{
				Group:    "example.org",
				Version:  "v1",
				Resource: "objects",
			})),
				field.Invalid(
					field.NewPath("spec"),
					"example.org.v1.objects",
					"CachedResource with this GVR already exists in the \"cluster-2\" workspace"),
			),
			cluster: logicalcluster.Name("cluster-2"),
		},
		"IgnoreIfNotCreate": {
			attr: updateAttr(createCachedResource("wohoo", schema.GroupVersionResource{
				Group:    "example.org",
				Version:  "v1",
				Resource: "objects",
			})),
			index: map[logicalcluster.Name]map[schema.GroupVersionResource][]*cachev1alpha1.CachedResource{
				"cluster-1": {
					schema.GroupVersionResource{
						Group:    "example.org",
						Version:  "v1",
						Resource: "objects",
					}: []*cachev1alpha1.CachedResource{
						createCachedResource("cluster-1-example-org-v1-objects", schema.GroupVersionResource{
							Group:    "example.org",
							Version:  "v1",
							Resource: "objects",
						}),
					},
				},
			},
			wantErr: nil,
			cluster: logicalcluster.Name("cluster-1"),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := genericapirequest.WithCluster(context.Background(), genericapirequest.Cluster{Name: tc.cluster})
			plugin := CachedResourceAdmission{
				listCachedResourcesByGVR: func(cluster logicalcluster.Name, gvr schema.GroupVersionResource) ([]*cachev1alpha1.CachedResource, error) {
					return tc.index[cluster][gvr], nil
				},
				dynamicRESTMapper: dynamicrestmapper.NewDynamicRESTMapper(),
			}

			err := plugin.Validate(ctx, tc.attr, nil)
			if tc.wantErr == nil {
				require.NoError(t, err, "Validate should succeed")
			} else {
				require.Equal(t, tc.wantErr, err, "Validate returned an unexpected error")
			}
		})
	}
}
