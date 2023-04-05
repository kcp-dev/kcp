/*
Copyright 2023 The KCP Authors.

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

package informer

import (
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

// ClusterLister is a cluster-aware Lister API.
type ClusterLister[R runtime.Object, L Lister[R]] interface {
	Cluster(name logicalcluster.Name) L
}

// Lister is a Lister API with generics.
type Lister[R runtime.Object] interface {
	List(labels.Selector) ([]R, error)
}

// FallbackListFunc is a function that returns []R from either local or global informer Listers based on a label selector.
type FallbackListFunc[R runtime.Object] func(selector labels.Selector) ([]R, error)

// ClusterGetter is a cluster-aware Getter API.
type ClusterGetter[R runtime.Object, G Getter[R]] interface {
	Cluster(name logicalcluster.Name) G
}

// Getter is a GetAPI call with generics.
type Getter[R runtime.Object] interface {
	Get(name string) (R, error)
}

// FallbackGetFunc is a function that returns an instance of R from either local or global informer Listers based on a name.
type FallbackGetFunc[R runtime.Object] func(name string) (R, error)

// ScopedFallbackGetFunc is a function that returns an instance of R from either local or global cluster-scoped informer Listers based on a name.
type ScopedFallbackGetFunc[R runtime.Object] func(clusterName logicalcluster.Name, name string) (R, error)

// NewListerWithFallback creates a new FallbackListFunc that looks up an object of type R first in the local lister, then in the global lister if no local results are found.
func NewListerWithFallback[R runtime.Object](localLister Lister[R], globalLister Lister[R]) FallbackListFunc[R] {
	return func(selector labels.Selector) ([]R, error) {
		r, err := localLister.List(selector)
		if len(r) == 0 {
			return globalLister.List(selector)
		}
		if err != nil {
			return nil, err
		}
		return r, nil
	}
}

// NewGetterWithFallback creates a new FallbackGetFunc that gets an object of type R first looking in the local lister, then in the global lister if not found.
func NewGetterWithFallback[R runtime.Object](localGetter, globalGetter Getter[R]) FallbackGetFunc[R] {
	return func(name string) (R, error) {
		var r R
		r, err := localGetter.Get(name)
		if errors.IsNotFound(err) {
			return globalGetter.Get(name)
		}
		if err != nil {
			// r will be nil in this case
			return r, err
		}
		return r, nil
	}
}

// NewScopedGetterWithFallback creates a new ScopedFallbackGetFunc that gets an object of type R within a given cluster path. The local lister is
// checked first, and if nothing is found, the global lister is checked.
func NewScopedGetterWithFallback[R runtime.Object, G Getter[R]](localGetter, globalGetter ClusterGetter[R, G]) ScopedFallbackGetFunc[R] {
	return func(clusterName logicalcluster.Name, name string) (R, error) {
		var r R
		r, err := localGetter.Cluster(clusterName).Get(name)
		if errors.IsNotFound(err) {
			return globalGetter.Cluster(clusterName).Get(name)
		}
		if err != nil {
			return r, err
		}
		return r, nil
	}
}
