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
	"fmt"
	"strconv"
	"sync"

	"github.com/kcp-dev/logicalcluster"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
)

type shardedStorage struct {
	storageBase
}

func (s *shardedStorage) Watch(ctx context.Context, options *internalversion.ListOptions) (watch.Interface, error) {
	if requestInfo, ok := request.RequestInfoFrom(ctx); ok && requestInfo.Namespace != "" {
		return nil, fmt.Errorf("cross-cluster calls cannot specify namespace")
	}

	var shardIdentifiers []string
	for name := range s.shards {
		shardIdentifiers = append(shardIdentifiers, name)
	}
	state, err := NewResourceVersionState(options.ResourceVersion, shardIdentifiers, s.shardIdentifierResourceVersion)
	if err != nil {
		return nil, fmt.Errorf("failed to parse sharded resource version state: %w", err)
	}

	watchers := map[string]watch.Interface{}
	for i := range state.ResourceVersions {
		client, err := s.clientFor(s.shards[state.ResourceVersions[i].Identifier])
		if err != nil {
			return nil, fmt.Errorf("failed to create sharded client: %w", err)
		}
		request, err := s.requestFor(client)
		if err != nil {
			return nil, fmt.Errorf("failed to create sharded request: %w", err)
		}
		request.OverwriteParam("limit", "500")
		request.OverwriteParam("resourceVersion", strconv.FormatInt(state.ResourceVersions[i].ResourceVersion, 10))
		request.SetHeader(logicalcluster.ClusterHeader, "*")
		watcher, err := request.Watch(ctx)
		if err != nil {
			return nil, fmt.Errorf("error executing watch request: %w", err)
		}
		watchers[state.ResourceVersions[i].Identifier] = watcher
	}

	return NewAggregateWatcher(state, watchers), nil
}

type stopper interface {
	Stop()
}

type aggregateWatcher struct {
	delegates []stopper
	wg        *sync.WaitGroup
	events    chan watch.Event

	state *ShardedResourceVersions
	lock  *sync.Mutex
}

func (a *aggregateWatcher) Stop() {
	for i := range a.delegates {
		a.delegates[i].Stop()
	}
	a.wg.Wait()
	close(a.events)
}

func (a *aggregateWatcher) ResultChan() <-chan watch.Event {
	return a.events
}

func (a *aggregateWatcher) process(identifier string, event watch.Event) {
	obj, ok := event.Object.(metav1.Common)
	if !ok {
		a.events <- watch.Event{
			Type:   watch.Error,
			Object: &errors.NewInternalError(fmt.Errorf("watch event contained a %T which could not cast to metav1.Common", event.Object)).ErrStatus,
		}
		return
	}
	a.lock.Lock()
	err := a.state.UpdateWith(identifier, obj)
	a.lock.Unlock()
	if err != nil {
		a.events <- watch.Event{
			Type:   watch.Error,
			Object: &errors.NewInternalError(fmt.Errorf("failed to update resource version vector clock: %w", err)).ErrStatus,
		}
		return
	}
	encoded, err := a.state.Encode()
	if err != nil {
		a.events <- watch.Event{
			Type:   watch.Error,
			Object: &errors.NewInternalError(fmt.Errorf("failed to encode resource version vector clock: %w", err)).ErrStatus,
		}
		return
	}
	obj.SetResourceVersion(encoded)
	a.events <- event
}

func NewAggregateWatcher(state *ShardedResourceVersions, delegates map[string]watch.Interface) watch.Interface {
	w := &aggregateWatcher{
		delegates: []stopper{},
		events:    make(chan watch.Event),
		wg:        &sync.WaitGroup{},
		state:     state,
		lock:      &sync.Mutex{},
	}
	for identifier := range delegates {
		w.wg.Add(1)
		go func(identifier string, events <-chan watch.Event) {
			defer utilruntime.HandleCrash()
			defer w.wg.Done()
			for event := range events {
				w.process(identifier, event)
			}
		}(identifier, delegates[identifier].ResultChan())
		w.delegates = append(w.delegates, delegates[identifier])
	}
	return w
}

var _ watch.Interface = &aggregateWatcher{}

func (s *shardedStorage) List(ctx context.Context, options *internalversion.ListOptions) (runtime.Object, error) {
	if requestInfo, ok := request.RequestInfoFrom(ctx); ok && requestInfo.Namespace != "" {
		return nil, fmt.Errorf("cross-cluster calls cannot specify namespace")
	}

	clientWantsPaging := options.Limit != 0
	var shardIdentifiers []string
	for name := range s.shards {
		shardIdentifiers = append(shardIdentifiers, name)
	}
	state, err := NewChunkedState(options.Continue, shardIdentifiers, s.shardIdentifierResourceVersion)
	if err != nil {
		return nil, fmt.Errorf("failed to parse sharded chunked state: %w", err)
	}
	var output *unstructured.UnstructuredList
	for {
		shard, continueToken, err := state.NextQuery()
		if err != nil {
			return nil, fmt.Errorf("failed to determine next sharded chunked query: %w", err)
		}
		client, err := s.clientFor(s.shards[shard])
		if err != nil {
			return nil, fmt.Errorf("failed to create sharded client: %w", err)
		}
		request, err := s.requestFor(client)
		if err != nil {
			return nil, fmt.Errorf("failed to create sharded request: %w", err)
		}
		request.OverwriteParam("limit", strconv.FormatInt(options.Limit, 10))
		request.OverwriteParam("continue", continueToken)
		request.SetHeader(logicalcluster.ClusterHeader, "*")
		result, err := request.Do(ctx).Get()
		if err != nil {
			return nil, fmt.Errorf("failed to get data from shard %q: %w", shard, err)
		}
		var list *unstructured.UnstructuredList
		switch r := result.(type) {
		case *unstructured.UnstructuredList:
			list = r
		case *unstructured.Unstructured:
			var conversionErr error
			list, conversionErr = r.ToList()
			if conversionErr != nil {
				return nil, fmt.Errorf("could not convert sharded response from %T to a list: %w", result, conversionErr)
			}
		default:
			return nil, fmt.Errorf("could not parse sharded response as a list, got %T", result)
		}
		if err := state.UpdateWith(shard, list); err != nil {
			return nil, fmt.Errorf("could not update sharded state: %w", err)
		}
		updatedResourceVersion, err := state.ToResourceVersions().Encode()
		if err != nil {
			return nil, fmt.Errorf("could not format new resource version: %w", err)
		}
		list.SetResourceVersion(updatedResourceVersion)
		for i := range list.Items {
			uniqueVersion := state.ToResourceVersions()
			if err := uniqueVersion.UpdateWith(shard, &list.Items[i]); err != nil {
				return nil, fmt.Errorf("could not update sharded state: %w", err)
			}
			updatedUniqueVersion, err := uniqueVersion.Encode()
			if err != nil {
				return nil, fmt.Errorf("could not format new resource version: %w", err)
			}
			list.Items[i].SetResourceVersion(updatedUniqueVersion)
		}
		updatedContinueToken, err := state.Encode()
		if err != nil {
			return nil, fmt.Errorf("could not format new continue token: %w", err)
		}
		list.SetContinue(updatedContinueToken)
		if output == nil {
			output = list
		} else {
			output.SetResourceVersion(updatedResourceVersion)
			output.Items = append(output.Items, list.Items...)
		}
		if clientWantsPaging || updatedContinueToken == "" {
			break
		}
	}
	return output, nil
}

type listWatcher interface {
	rest.Lister
	rest.Watcher
}

var _ listWatcher = &shardedStorage{}
