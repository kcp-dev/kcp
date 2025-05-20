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

package dynamiclister

import (
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/dynamiclister"
	"k8s.io/client-go/tools/cache"
)

// NewRuntimeObjectShim returns a new shim for ClusterLister.
// It wraps Lister so that it implements kcpcache.GenericClusterLister interface.
func NewRuntimeObjectShim(lister ClusterLister) kcpcache.GenericClusterLister {
	return &dynamicClusterListerShim{lister: lister}
}

var _ kcpcache.GenericClusterLister = &dynamicClusterListerShim{}

type dynamicClusterListerShim struct {
	lister ClusterLister
}

func (s *dynamicClusterListerShim) List(selector labels.Selector) (ret []runtime.Object, err error) {
	objs, err := s.lister.List(selector)
	if err != nil {
		return nil, err
	}

	ret = make([]runtime.Object, len(objs))
	for index, obj := range objs {
		ret[index] = obj
	}
	return ret, err
}

func (s *dynamicClusterListerShim) ByCluster(clusterName logicalcluster.Name) cache.GenericLister {
	return dynamiclister.NewRuntimeObjectShim(s.lister.Cluster(clusterName))
}
