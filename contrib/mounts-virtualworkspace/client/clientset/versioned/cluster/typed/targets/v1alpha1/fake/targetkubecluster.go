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

	targetsv1alpha1 "github.com/kcp-dev/kcp/contrib/mounts-virtualworkspace/apis/targets/v1alpha1"
	applyconfigurationstargetsv1alpha1 "github.com/kcp-dev/kcp/contrib/mounts-virtualworkspace/client/applyconfiguration/targets/v1alpha1"
	targetsv1alpha1client "github.com/kcp-dev/kcp/contrib/mounts-virtualworkspace/client/clientset/versioned/typed/targets/v1alpha1"
)

var targetKubeClustersResource = schema.GroupVersionResource{Group: "targets.contrib.kcp.io", Version: "v1alpha1", Resource: "targetkubeclusters"}
var targetKubeClustersKind = schema.GroupVersionKind{Group: "targets.contrib.kcp.io", Version: "v1alpha1", Kind: "TargetKubeCluster"}

type targetKubeClustersClusterClient struct {
	*kcptesting.Fake
}

// Cluster scopes the client down to a particular cluster.
func (c *targetKubeClustersClusterClient) Cluster(clusterPath logicalcluster.Path) targetsv1alpha1client.TargetKubeClusterInterface {
	if clusterPath == logicalcluster.Wildcard {
		panic("A specific cluster must be provided when scoping, not the wildcard.")
	}

	return &targetKubeClustersClient{Fake: c.Fake, ClusterPath: clusterPath}
}

// List takes label and field selectors, and returns the list of TargetKubeClusters that match those selectors across all clusters.
func (c *targetKubeClustersClusterClient) List(ctx context.Context, opts metav1.ListOptions) (*targetsv1alpha1.TargetKubeClusterList, error) {
	obj, err := c.Fake.Invokes(kcptesting.NewRootListAction(targetKubeClustersResource, targetKubeClustersKind, logicalcluster.Wildcard, opts), &targetsv1alpha1.TargetKubeClusterList{})
	if obj == nil {
		return nil, err
	}

	label, _, _ := testing.ExtractFromListOptions(opts)
	if label == nil {
		label = labels.Everything()
	}
	list := &targetsv1alpha1.TargetKubeClusterList{ListMeta: obj.(*targetsv1alpha1.TargetKubeClusterList).ListMeta}
	for _, item := range obj.(*targetsv1alpha1.TargetKubeClusterList).Items {
		if label.Matches(labels.Set(item.Labels)) {
			list.Items = append(list.Items, item)
		}
	}
	return list, err
}

// Watch returns a watch.Interface that watches the requested TargetKubeClusters across all clusters.
func (c *targetKubeClustersClusterClient) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	return c.Fake.InvokesWatch(kcptesting.NewRootWatchAction(targetKubeClustersResource, logicalcluster.Wildcard, opts))
}

type targetKubeClustersClient struct {
	*kcptesting.Fake
	ClusterPath logicalcluster.Path
}

func (c *targetKubeClustersClient) Create(ctx context.Context, targetKubeCluster *targetsv1alpha1.TargetKubeCluster, opts metav1.CreateOptions) (*targetsv1alpha1.TargetKubeCluster, error) {
	obj, err := c.Fake.Invokes(kcptesting.NewRootCreateAction(targetKubeClustersResource, c.ClusterPath, targetKubeCluster), &targetsv1alpha1.TargetKubeCluster{})
	if obj == nil {
		return nil, err
	}
	return obj.(*targetsv1alpha1.TargetKubeCluster), err
}

func (c *targetKubeClustersClient) Update(ctx context.Context, targetKubeCluster *targetsv1alpha1.TargetKubeCluster, opts metav1.UpdateOptions) (*targetsv1alpha1.TargetKubeCluster, error) {
	obj, err := c.Fake.Invokes(kcptesting.NewRootUpdateAction(targetKubeClustersResource, c.ClusterPath, targetKubeCluster), &targetsv1alpha1.TargetKubeCluster{})
	if obj == nil {
		return nil, err
	}
	return obj.(*targetsv1alpha1.TargetKubeCluster), err
}

func (c *targetKubeClustersClient) UpdateStatus(ctx context.Context, targetKubeCluster *targetsv1alpha1.TargetKubeCluster, opts metav1.UpdateOptions) (*targetsv1alpha1.TargetKubeCluster, error) {
	obj, err := c.Fake.Invokes(kcptesting.NewRootUpdateSubresourceAction(targetKubeClustersResource, c.ClusterPath, "status", targetKubeCluster), &targetsv1alpha1.TargetKubeCluster{})
	if obj == nil {
		return nil, err
	}
	return obj.(*targetsv1alpha1.TargetKubeCluster), err
}

func (c *targetKubeClustersClient) Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	_, err := c.Fake.Invokes(kcptesting.NewRootDeleteActionWithOptions(targetKubeClustersResource, c.ClusterPath, name, opts), &targetsv1alpha1.TargetKubeCluster{})
	return err
}

func (c *targetKubeClustersClient) DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error {
	action := kcptesting.NewRootDeleteCollectionAction(targetKubeClustersResource, c.ClusterPath, listOpts)

	_, err := c.Fake.Invokes(action, &targetsv1alpha1.TargetKubeClusterList{})
	return err
}

func (c *targetKubeClustersClient) Get(ctx context.Context, name string, options metav1.GetOptions) (*targetsv1alpha1.TargetKubeCluster, error) {
	obj, err := c.Fake.Invokes(kcptesting.NewRootGetAction(targetKubeClustersResource, c.ClusterPath, name), &targetsv1alpha1.TargetKubeCluster{})
	if obj == nil {
		return nil, err
	}
	return obj.(*targetsv1alpha1.TargetKubeCluster), err
}

// List takes label and field selectors, and returns the list of TargetKubeClusters that match those selectors.
func (c *targetKubeClustersClient) List(ctx context.Context, opts metav1.ListOptions) (*targetsv1alpha1.TargetKubeClusterList, error) {
	obj, err := c.Fake.Invokes(kcptesting.NewRootListAction(targetKubeClustersResource, targetKubeClustersKind, c.ClusterPath, opts), &targetsv1alpha1.TargetKubeClusterList{})
	if obj == nil {
		return nil, err
	}

	label, _, _ := testing.ExtractFromListOptions(opts)
	if label == nil {
		label = labels.Everything()
	}
	list := &targetsv1alpha1.TargetKubeClusterList{ListMeta: obj.(*targetsv1alpha1.TargetKubeClusterList).ListMeta}
	for _, item := range obj.(*targetsv1alpha1.TargetKubeClusterList).Items {
		if label.Matches(labels.Set(item.Labels)) {
			list.Items = append(list.Items, item)
		}
	}
	return list, err
}

func (c *targetKubeClustersClient) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	return c.Fake.InvokesWatch(kcptesting.NewRootWatchAction(targetKubeClustersResource, c.ClusterPath, opts))
}

func (c *targetKubeClustersClient) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*targetsv1alpha1.TargetKubeCluster, error) {
	obj, err := c.Fake.Invokes(kcptesting.NewRootPatchSubresourceAction(targetKubeClustersResource, c.ClusterPath, name, pt, data, subresources...), &targetsv1alpha1.TargetKubeCluster{})
	if obj == nil {
		return nil, err
	}
	return obj.(*targetsv1alpha1.TargetKubeCluster), err
}

func (c *targetKubeClustersClient) Apply(ctx context.Context, applyConfiguration *applyconfigurationstargetsv1alpha1.TargetKubeClusterApplyConfiguration, opts metav1.ApplyOptions) (*targetsv1alpha1.TargetKubeCluster, error) {
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
	obj, err := c.Fake.Invokes(kcptesting.NewRootPatchSubresourceAction(targetKubeClustersResource, c.ClusterPath, *name, types.ApplyPatchType, data), &targetsv1alpha1.TargetKubeCluster{})
	if obj == nil {
		return nil, err
	}
	return obj.(*targetsv1alpha1.TargetKubeCluster), err
}

func (c *targetKubeClustersClient) ApplyStatus(ctx context.Context, applyConfiguration *applyconfigurationstargetsv1alpha1.TargetKubeClusterApplyConfiguration, opts metav1.ApplyOptions) (*targetsv1alpha1.TargetKubeCluster, error) {
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
	obj, err := c.Fake.Invokes(kcptesting.NewRootPatchSubresourceAction(targetKubeClustersResource, c.ClusterPath, *name, types.ApplyPatchType, data, "status"), &targetsv1alpha1.TargetKubeCluster{})
	if obj == nil {
		return nil, err
	}
	return obj.(*targetsv1alpha1.TargetKubeCluster), err
}
