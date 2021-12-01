/*
Copyright 2021 The KCP Authors.

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

package apiserver

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	clientrest "k8s.io/client-go/rest"
	"k8s.io/kubernetes/pkg/genericcontrolplane"
)

// delegatingStorage delegates requests off to individual shards based on the cluster
// for which the request was made.
type delegatingStorage struct {
	storageBase
}

func (s *delegatingStorage) Create(ctx context.Context, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
	return s.routedResponse(ctx, nil, false)
}

func (s *delegatingStorage) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	return s.routedResponse(ctx, &options.ResourceVersion, false)
}

func (s *delegatingStorage) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
	var resourceVersion *string
	if preconditions := objInfo.Preconditions(); preconditions != nil {
		resourceVersion = preconditions.ResourceVersion // TODO: this always seems to be nil ... ?
	}
	resp, err := s.routedResponse(ctx, resourceVersion, objInfo.Preconditions() != nil)
	return resp, false, err
}

func (s *delegatingStorage) Watch(ctx context.Context, options *internalversion.ListOptions) (watch.Interface, error) {
	req, updater, err := s.routedRequest(ctx, &options.ResourceVersion, false)
	if err != nil {
		return nil, err
	}
	watcher, err := req.Watch(ctx)
	if err != nil {
		return nil, err
	}
	return mutatingWatcherFor(watcher, updater), nil
}

func (s *delegatingStorage) List(ctx context.Context, options *internalversion.ListOptions) (runtime.Object, error) {
	return s.routedResponse(ctx, &options.ResourceVersion, false)
}

func (s *delegatingStorage) Delete(ctx context.Context, name string, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions) (runtime.Object, bool, error) {
	var resourceVersion *string
	if options.Preconditions != nil {
		resourceVersion = options.Preconditions.ResourceVersion
	}
	resp, err := s.routedResponse(ctx, resourceVersion, resourceVersion != nil)
	return resp, false, err
}

func (s *delegatingStorage) DeleteCollection(ctx context.Context, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions, listOptions *internalversion.ListOptions) (runtime.Object, error) {
	var resourceVersion *string
	if options.Preconditions != nil {
		resourceVersion = options.Preconditions.ResourceVersion
	}
	return s.routedResponse(ctx, resourceVersion, resourceVersion != nil)
}

var _ rest.StandardStorage = &delegatingStorage{}

// routedResponse routes a request to a shard and returns the response.
func (s *delegatingStorage) routedResponse(ctx context.Context, resourceVersion *string, mutateBody bool) (runtime.Object, error) {
	request, updater, err := s.routedRequest(ctx, resourceVersion, mutateBody)
	if err != nil {
		return nil, err
	}
	r, e := request.Do(ctx).Get()
	var statusError *k8serrors.StatusError
	if errors.As(e, &statusError) {
		// prefer to respond with the error the server gave us
		return &statusError.ErrStatus, nil
	}
	if e != nil {
		return r, e
	}
	if err := updater(r); err != nil {
		e = fmt.Errorf("failed to update response: %w", err)
	}
	return r, e
}

// routedRequest determines which shard to route a request to by reading the cluster field on the object.
func (s *delegatingStorage) routedRequest(ctx context.Context, resourceVersion *string, mutateBody bool) (*clientrest.Request, func(runtime.Object) error, error) {
	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return nil, nil, err
	}
	identifier, _, err := genericcontrolplane.ParseClusterName(clusterName)
	if err != nil {
		return nil, nil, err
	}

	cfg, exists := s.shards[identifier]
	if !exists {
		return nil, nil, fmt.Errorf("no such shard %v", identifier)
	}
	client, err := s.clientFor(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create sharded client: %w", err)
	}
	rv, err := collapseResourceVersion(resourceVersion, identifier)
	if err != nil {
		return nil, nil, fmt.Errorf("could not collapse complex resource version: %w", err)
	}
	var mutators []func(runtime.Object) error
	if mutateBody {
		mutators = append(mutators, mutateInputResourceVersion(identifier))
	}
	request, err := s.requestFor(client, mutators...)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create sharded request: %w", err)
	}
	request.OverwriteParam("resourceVersion", rv)
	request.SetHeader("X-Kubernetes-Cluster", clusterName)
	return request, mutateOutputResourceVersion(identifier), nil
}

func mutateInputResourceVersion(identifier string) func(runtime.Object) error {
	return func(obj runtime.Object) error {
		switch r := obj.(type) {
		case *unstructured.Unstructured:
			if r.GetKind() == "DeleteOptions" {
				o := r.UnstructuredContent()
				complexRv, found, err := unstructured.NestedString(o, "preconditions", "resourceVersion")
				if err != nil {
					return fmt.Errorf("could not extract resourceVersion: %w", err)
				}
				if !found {
					complexRv = ""
				}
				resourceVersion, err := collapseResourceVersion(&complexRv, identifier)
				if err != nil {
					return fmt.Errorf("could not collapse complex resource version: %w", err)
				}
				if err != nil {
					return fmt.Errorf("could not read resourceVersion from DeleteOptions: %w", err)
				}
				if err := unstructured.SetNestedField(o, resourceVersion, "preconditions", "resourceVersion"); err != nil {
					return fmt.Errorf("could not set resourceVersion in DeleteOptions: %w", err)
				}
			} else {
				complexRv := r.GetResourceVersion()
				resourceVersion, err := collapseResourceVersion(&complexRv, identifier)
				if err != nil {
					return fmt.Errorf("could not collapse complex resource version: %w", err)
				}
				r.SetResourceVersion(resourceVersion)
			}
		default:
			return fmt.Errorf("unexpected input type: %T", obj)
		}
		return nil
	}
}

// collapseResourceVersion collapses a complex resource version to a shard-specific one.
// As even single-logical-cluster clients pass a complex resource version on all objects,
// we need to collapse to something an individual shard can understand before delegating.
func collapseResourceVersion(complexResourceVersion *string, identifier string) (string, error) {
	if complexResourceVersion == nil {
		return "", nil
	}
	state, err := NewResourceVersionState(*complexResourceVersion, []string{identifier}, 0)
	if err != nil {
		return "", fmt.Errorf("failed to parse sharded resource version state: %w", err)
	}
	resourceVersion := int64(-1)
	for _, item := range state.ResourceVersions {
		if item.Identifier == identifier {
			resourceVersion = item.ResourceVersion
		}
	}
	if resourceVersion < 0 {
		return "", fmt.Errorf("sharded resource version did not contain shard %s", identifier)
	}
	return strconv.FormatInt(resourceVersion, 10), nil
}

func mutateOutputResourceVersion(identifier string) func(runtime.Object) error {
	return func(obj runtime.Object) error {
		switch r := obj.(type) {
		case *unstructured.UnstructuredList:
			if err := inflateResourceVersion(r, identifier); err != nil {
				return err
			}
			for i := range r.Items {
				if err := inflateResourceVersion(&r.Items[i], identifier); err != nil {
					return err
				}
			}
		case *unstructured.Unstructured:
			return inflateResourceVersion(r, identifier)
		default:
			// this is not an API object, forget about it
		}
		return nil
	}
}

// inflateResourceVersion inflates a simple resource version from one shard to a complex one.
func inflateResourceVersion(obj runtime.Object, identifier string) error {
	mutable, ok := obj.(metav1.Common)
	if !ok {
		return fmt.Errorf("could not cast %T to metav1.Common", obj)
	}
	if mutable.GetResourceVersion() == "" {
		// this is not an API object, forget about it
		return nil
	}
	state, err := NewResourceVersionState("", []string{identifier}, 0)
	if err != nil {
		return fmt.Errorf("failed to parse sharded resource version state: %w", err)
	}
	if err := state.UpdateWith(identifier, mutable); err != nil {
		return fmt.Errorf("could not update complex resource version: %w", err)
	}
	encoded, err := state.Encode()
	if err != nil {
		return fmt.Errorf("could not encode complex resource version: %w", err)
	}
	mutable.SetResourceVersion(encoded)
	return nil
}

// mutatingWatcherFor wraps a source watcher with a mutator func which attempts to mutate all objects sent to the watch stream
func mutatingWatcherFor(source watch.Interface, mutator func(runtime.Object) error) watch.Interface {
	w := mutatingWatcher{
		mutator: mutator,
		source:  source,
		output:  make(chan watch.Event),
		wg:      &sync.WaitGroup{},
	}
	w.wg.Add(1)
	go func(input <-chan watch.Event, output chan<- watch.Event) {
		defer w.wg.Done()
		for event := range input {
			if err := mutator(event.Object); err != nil {
				output <- watch.Event{
					Type:   watch.Error,
					Object: &k8serrors.NewInternalError(fmt.Errorf("failed to mutate object in watch event: %w", err)).ErrStatus,
				}
			} else {
				output <- event
			}
		}
	}(source.ResultChan(), w.output)
	return &w
}

type mutatingWatcher struct {
	mutator func(runtime.Object) error
	source  watch.Interface
	output  chan watch.Event
	wg      *sync.WaitGroup
}

func (m *mutatingWatcher) Stop() {
	m.source.Stop()
	m.wg.Wait()
	close(m.output)
}

func (m *mutatingWatcher) ResultChan() <-chan watch.Event {
	return m.output
}

var _ watch.Interface = &mutatingWatcher{}
