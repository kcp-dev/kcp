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

package kubequota

import (
	"sync"

	"github.com/kcp-dev/logicalcluster"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
)

type delegatingEventHandler struct {
	lock          sync.RWMutex
	eventHandlers map[schema.GroupResource]map[logicalcluster.Name]cache.ResourceEventHandler
}

func (d *delegatingEventHandler) registerEventHandler(
	resource schema.GroupResource,
	informer cache.SharedIndexInformer,
	clusterName logicalcluster.Name,
	h cache.ResourceEventHandler,
) {
	d.lock.Lock()
	defer d.lock.Unlock()

	groupResourceHandlers, ok := d.eventHandlers[resource]
	if !ok {
		groupResourceHandlers = map[logicalcluster.Name]cache.ResourceEventHandler{}
		d.eventHandlers[resource] = groupResourceHandlers

		informer.AddEventHandler(
			cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					h := d.getEventHandler(resource, clusterNameForObj(obj))
					if h == nil {
						return
					}
					h.OnAdd(obj)
				},
				UpdateFunc: func(oldObj, newObj interface{}) {
					h := d.getEventHandler(resource, clusterNameForObj(oldObj))
					if h == nil {
						return
					}
					h.OnUpdate(oldObj, newObj)
				},
				DeleteFunc: func(obj interface{}) {
					h := d.getEventHandler(resource, clusterNameForObj(obj))
					if h == nil {
						return
					}
					h.OnDelete(obj)
				},
			},
		)
	}

	groupResourceHandlers[clusterName] = h

	// Similar to what a running shared informer does when an event handler is added, send synthetic add events
	// for everything currently in the cache.
	go func() {
		h := d.getEventHandler(resource, clusterName)
		if h == nil {
			return
		}
		list := informer.GetStore().List()
		for i := range list {
			obj := list[i]
			h.OnAdd(obj)
		}
	}()
}

func (d *delegatingEventHandler) getEventHandler(resource schema.GroupResource, clusterName logicalcluster.Name) cache.ResourceEventHandler {
	if clusterName.Empty() {
		return nil
	}

	d.lock.RLock()
	defer d.lock.RUnlock()

	groupResourceHandlers, ok := d.eventHandlers[resource]
	if !ok {
		return nil
	}

	return groupResourceHandlers[clusterName]
}
