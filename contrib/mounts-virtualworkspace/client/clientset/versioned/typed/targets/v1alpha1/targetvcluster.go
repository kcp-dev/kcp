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
	"context"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	gentype "k8s.io/client-go/gentype"

	v1alpha1 "github.com/kcp-dev/kcp/contrib/mounts-virtualworkspace/apis/targets/v1alpha1"
	targetsv1alpha1 "github.com/kcp-dev/kcp/contrib/mounts-virtualworkspace/client/applyconfiguration/targets/v1alpha1"
	scheme "github.com/kcp-dev/kcp/contrib/mounts-virtualworkspace/client/clientset/versioned/scheme"
)

// TargetVClustersGetter has a method to return a TargetVClusterInterface.
// A group's client should implement this interface.
type TargetVClustersGetter interface {
	TargetVClusters() TargetVClusterInterface
}

// TargetVClusterInterface has methods to work with TargetVCluster resources.
type TargetVClusterInterface interface {
	Create(ctx context.Context, targetVCluster *v1alpha1.TargetVCluster, opts v1.CreateOptions) (*v1alpha1.TargetVCluster, error)
	Update(ctx context.Context, targetVCluster *v1alpha1.TargetVCluster, opts v1.UpdateOptions) (*v1alpha1.TargetVCluster, error)
	// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().
	UpdateStatus(ctx context.Context, targetVCluster *v1alpha1.TargetVCluster, opts v1.UpdateOptions) (*v1alpha1.TargetVCluster, error)
	Delete(ctx context.Context, name string, opts v1.DeleteOptions) error
	DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error
	Get(ctx context.Context, name string, opts v1.GetOptions) (*v1alpha1.TargetVCluster, error)
	List(ctx context.Context, opts v1.ListOptions) (*v1alpha1.TargetVClusterList, error)
	Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error)
	Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *v1alpha1.TargetVCluster, err error)
	Apply(ctx context.Context, targetVCluster *targetsv1alpha1.TargetVClusterApplyConfiguration, opts v1.ApplyOptions) (result *v1alpha1.TargetVCluster, err error)
	// Add a +genclient:noStatus comment above the type to avoid generating ApplyStatus().
	ApplyStatus(ctx context.Context, targetVCluster *targetsv1alpha1.TargetVClusterApplyConfiguration, opts v1.ApplyOptions) (result *v1alpha1.TargetVCluster, err error)
	TargetVClusterExpansion
}

// targetVClusters implements TargetVClusterInterface
type targetVClusters struct {
	*gentype.ClientWithListAndApply[*v1alpha1.TargetVCluster, *v1alpha1.TargetVClusterList, *targetsv1alpha1.TargetVClusterApplyConfiguration]
}

// newTargetVClusters returns a TargetVClusters
func newTargetVClusters(c *TargetsV1alpha1Client) *targetVClusters {
	return &targetVClusters{
		gentype.NewClientWithListAndApply[*v1alpha1.TargetVCluster, *v1alpha1.TargetVClusterList, *targetsv1alpha1.TargetVClusterApplyConfiguration](
			"targetvclusters",
			c.RESTClient(),
			scheme.ParameterCodec,
			"",
			func() *v1alpha1.TargetVCluster { return &v1alpha1.TargetVCluster{} },
			func() *v1alpha1.TargetVClusterList { return &v1alpha1.TargetVClusterList{} }),
	}
}
