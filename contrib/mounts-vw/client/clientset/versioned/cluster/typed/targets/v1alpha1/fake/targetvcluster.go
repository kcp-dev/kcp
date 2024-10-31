//go:build !ignore_autogenerated
// +build !ignore_autogenerated

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

// Code generated by kcp code-generator. DO NOT EDIT.

package fake

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kcp-dev/logicalcluster/v3"

	kcptesting "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/testing"

	targetsv1alpha1 "github.com/kcp-dev/kcp/contrib/mounts-vw/apis/targets/v1alpha1"
	applyconfigurationstargetsv1alpha1 "github.com/kcp-dev/kcp/contrib/mounts-vw/client/applyconfiguration/targets/v1alpha1"
	targetsv1alpha1client "github.com/kcp-dev/kcp/contrib/mounts-vw/client/clientset/versioned/typed/targets/v1alpha1"
)

var targetVClustersResource = schema.GroupVersionResource{Group: "targets.contrib.kcp.io", Version: "v1alpha1", Resource: "targetvclusters"}
var targetVClustersKind = schema.GroupVersionKind{Group: "targets.contrib.kcp.io", Version: "v1alpha1", Kind: "TargetVCluster"}

type targetVClustersClusterClient struct {
	*kcptesting.Fake
}

// Cluster scopes the client down to a particular cluster.
func (c *targetVClustersClusterClient) Cluster(clusterPath logicalcluster.Path) targetsv1alpha1client.TargetVClusterInterface {
	if clusterPath == logicalcluster.Wildcard {
		panic("A specific cluster must be provided when scoping, not the wildcard.")
	}

	return &targetVClustersClient{Fake: c.Fake, ClusterPath: clusterPath}
}

// List takes label and field selectors, and returns the list of TargetVClusters that match those selectors across all clusters.
func (c *targetVClustersClusterClient) List(ctx context.Context, opts metav1.ListOptions) (*targetsv1alpha1.TargetVClusterList, error) {
	obj, err := c.Fake.Invokes(kcptesting.NewRootListAction(targetVClustersResource, targetVClustersKind, logicalcluster.Wildcard, opts), &targetsv1alpha1.TargetVClusterList{})
	if obj == nil {
		return nil, err
	}

	label, _, _ := testing.ExtractFromListOptions(opts)
	if label == nil {
		label = labels.Everything()
	}
	list := &targetsv1alpha1.TargetVClusterList{ListMeta: obj.(*targetsv1alpha1.TargetVClusterList).ListMeta}
	for _, item := range obj.(*targetsv1alpha1.TargetVClusterList).Items {
		if label.Matches(labels.Set(item.Labels)) {
			list.Items = append(list.Items, item)
		}
	}
	return list, err
}

// Watch returns a watch.Interface that watches the requested TargetVClusters across all clusters.
func (c *targetVClustersClusterClient) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	return c.Fake.InvokesWatch(kcptesting.NewRootWatchAction(targetVClustersResource, logicalcluster.Wildcard, opts))
}

type targetVClustersClient struct {
	*kcptesting.Fake
	ClusterPath logicalcluster.Path
}

func (c *targetVClustersClient) Create(ctx context.Context, targetVCluster *targetsv1alpha1.TargetVCluster, opts metav1.CreateOptions) (*targetsv1alpha1.TargetVCluster, error) {
	obj, err := c.Fake.Invokes(kcptesting.NewRootCreateAction(targetVClustersResource, c.ClusterPath, targetVCluster), &targetsv1alpha1.TargetVCluster{})
	if obj == nil {
		return nil, err
	}
	return obj.(*targetsv1alpha1.TargetVCluster), err
}

func (c *targetVClustersClient) Update(ctx context.Context, targetVCluster *targetsv1alpha1.TargetVCluster, opts metav1.UpdateOptions) (*targetsv1alpha1.TargetVCluster, error) {
	obj, err := c.Fake.Invokes(kcptesting.NewRootUpdateAction(targetVClustersResource, c.ClusterPath, targetVCluster), &targetsv1alpha1.TargetVCluster{})
	if obj == nil {
		return nil, err
	}
	return obj.(*targetsv1alpha1.TargetVCluster), err
}

func (c *targetVClustersClient) UpdateStatus(ctx context.Context, targetVCluster *targetsv1alpha1.TargetVCluster, opts metav1.UpdateOptions) (*targetsv1alpha1.TargetVCluster, error) {
	obj, err := c.Fake.Invokes(kcptesting.NewRootUpdateSubresourceAction(targetVClustersResource, c.ClusterPath, "status", targetVCluster), &targetsv1alpha1.TargetVCluster{})
	if obj == nil {
		return nil, err
	}
	return obj.(*targetsv1alpha1.TargetVCluster), err
}

func (c *targetVClustersClient) Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	_, err := c.Fake.Invokes(kcptesting.NewRootDeleteActionWithOptions(targetVClustersResource, c.ClusterPath, name, opts), &targetsv1alpha1.TargetVCluster{})
	return err
}

func (c *targetVClustersClient) DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error {
	action := kcptesting.NewRootDeleteCollectionAction(targetVClustersResource, c.ClusterPath, listOpts)

	_, err := c.Fake.Invokes(action, &targetsv1alpha1.TargetVClusterList{})
	return err
}

func (c *targetVClustersClient) Get(ctx context.Context, name string, options metav1.GetOptions) (*targetsv1alpha1.TargetVCluster, error) {
	obj, err := c.Fake.Invokes(kcptesting.NewRootGetAction(targetVClustersResource, c.ClusterPath, name), &targetsv1alpha1.TargetVCluster{})
	if obj == nil {
		return nil, err
	}
	return obj.(*targetsv1alpha1.TargetVCluster), err
}

// List takes label and field selectors, and returns the list of TargetVClusters that match those selectors.
func (c *targetVClustersClient) List(ctx context.Context, opts metav1.ListOptions) (*targetsv1alpha1.TargetVClusterList, error) {
	obj, err := c.Fake.Invokes(kcptesting.NewRootListAction(targetVClustersResource, targetVClustersKind, c.ClusterPath, opts), &targetsv1alpha1.TargetVClusterList{})
	if obj == nil {
		return nil, err
	}

	label, _, _ := testing.ExtractFromListOptions(opts)
	if label == nil {
		label = labels.Everything()
	}
	list := &targetsv1alpha1.TargetVClusterList{ListMeta: obj.(*targetsv1alpha1.TargetVClusterList).ListMeta}
	for _, item := range obj.(*targetsv1alpha1.TargetVClusterList).Items {
		if label.Matches(labels.Set(item.Labels)) {
			list.Items = append(list.Items, item)
		}
	}
	return list, err
}

func (c *targetVClustersClient) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	return c.Fake.InvokesWatch(kcptesting.NewRootWatchAction(targetVClustersResource, c.ClusterPath, opts))
}

func (c *targetVClustersClient) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*targetsv1alpha1.TargetVCluster, error) {
	obj, err := c.Fake.Invokes(kcptesting.NewRootPatchSubresourceAction(targetVClustersResource, c.ClusterPath, name, pt, data, subresources...), &targetsv1alpha1.TargetVCluster{})
	if obj == nil {
		return nil, err
	}
	return obj.(*targetsv1alpha1.TargetVCluster), err
}

func (c *targetVClustersClient) Apply(ctx context.Context, applyConfiguration *applyconfigurationstargetsv1alpha1.TargetVClusterApplyConfiguration, opts metav1.ApplyOptions) (*targetsv1alpha1.TargetVCluster, error) {
	if applyConfiguration == nil {
		return nil, fmt.Errorf("applyConfiguration provided to Apply must not be nil")
	}
	data, err := json.Marshal(applyConfiguration)
	if err != nil {
		return nil, err
	}
	name := applyConfiguration.Name
	if name == nil {
		return nil, fmt.Errorf("applyConfiguration.Name must be provided to Apply")
	}
	obj, err := c.Fake.Invokes(kcptesting.NewRootPatchSubresourceAction(targetVClustersResource, c.ClusterPath, *name, types.ApplyPatchType, data), &targetsv1alpha1.TargetVCluster{})
	if obj == nil {
		return nil, err
	}
	return obj.(*targetsv1alpha1.TargetVCluster), err
}

func (c *targetVClustersClient) ApplyStatus(ctx context.Context, applyConfiguration *applyconfigurationstargetsv1alpha1.TargetVClusterApplyConfiguration, opts metav1.ApplyOptions) (*targetsv1alpha1.TargetVCluster, error) {
	if applyConfiguration == nil {
		return nil, fmt.Errorf("applyConfiguration provided to Apply must not be nil")
	}
	data, err := json.Marshal(applyConfiguration)
	if err != nil {
		return nil, err
	}
	name := applyConfiguration.Name
	if name == nil {
		return nil, fmt.Errorf("applyConfiguration.Name must be provided to Apply")
	}
	obj, err := c.Fake.Invokes(kcptesting.NewRootPatchSubresourceAction(targetVClustersResource, c.ClusterPath, *name, types.ApplyPatchType, data, "status"), &targetsv1alpha1.TargetVCluster{})
	if obj == nil {
		return nil, err
	}
	return obj.(*targetsv1alpha1.TargetVCluster), err
}
