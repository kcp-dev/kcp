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

// Code generated by lister-gen. DO NOT EDIT.

package v1alpha1

import (
	"context"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"

	v1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
)

// LocationDomainLister helps list LocationDomains.
// All objects returned here must be treated as read-only.
type LocationDomainLister interface {
	// List lists all LocationDomains in the indexer.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*v1alpha1.LocationDomain, err error)
	// ListWithContext lists all LocationDomains in the indexer.
	// Objects returned here must be treated as read-only.
	ListWithContext(ctx context.Context, selector labels.Selector) (ret []*v1alpha1.LocationDomain, err error)
	// Get retrieves the LocationDomain from the index for a given name.
	// Objects returned here must be treated as read-only.
	Get(name string) (*v1alpha1.LocationDomain, error)
	// GetWithContext retrieves the LocationDomain from the index for a given name.
	// Objects returned here must be treated as read-only.
	GetWithContext(ctx context.Context, name string) (*v1alpha1.LocationDomain, error)
	LocationDomainListerExpansion
}

// locationDomainLister implements the LocationDomainLister interface.
type locationDomainLister struct {
	indexer cache.Indexer
}

// NewLocationDomainLister returns a new LocationDomainLister.
func NewLocationDomainLister(indexer cache.Indexer) LocationDomainLister {
	return &locationDomainLister{indexer: indexer}
}

// List lists all LocationDomains in the indexer.
func (s *locationDomainLister) List(selector labels.Selector) (ret []*v1alpha1.LocationDomain, err error) {
	return s.ListWithContext(context.Background(), selector)
}

// ListWithContext lists all LocationDomains in the indexer.
func (s *locationDomainLister) ListWithContext(ctx context.Context, selector labels.Selector) (ret []*v1alpha1.LocationDomain, err error) {
	err = cache.ListAll(s.indexer, selector, func(m interface{}) {
		ret = append(ret, m.(*v1alpha1.LocationDomain))
	})
	return ret, err
}

// Get retrieves the LocationDomain from the index for a given name.
func (s *locationDomainLister) Get(name string) (*v1alpha1.LocationDomain, error) {
	return s.GetWithContext(context.Background(), name)
}

// GetWithContext retrieves the LocationDomain from the index for a given name.
func (s *locationDomainLister) GetWithContext(ctx context.Context, name string) (*v1alpha1.LocationDomain, error) {
	obj, exists, err := s.indexer.GetByKey(name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(v1alpha1.Resource("locationdomain"), name)
	}
	return obj.(*v1alpha1.LocationDomain), nil
}
