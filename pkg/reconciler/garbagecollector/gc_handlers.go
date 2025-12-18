/*
Copyright 2025 The KCP Authors.

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

package garbagecollector

import (
	"context"
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/kcp/pkg/tombstone"
	"github.com/kcp-dev/kcp/pkg/virtual/apiexport/schemas/builtin"
)

func (gc *GarbageCollector) registerHandlers(ctx context.Context) func() {
	// TODO(ntnn): Handle sharding? Could add a filter on all watches to
	// only watch resources for logical clusters assigned to this shard.
	// Question is whether that even matters.
	// Also if the GC of all shards works against all logical clusters
	// it could help with balancing the gc load - but it could also
	// produce a lot of redundant work when the same objects are worked
	// on by multiple gc instances.

	// Start monitors for builtin APIs
	// TODO(ntnn): instead of making a wacky list of fake builtin crds
	// update monitors off of lists of GVRs and use a helper function to
	// produce the list of GVRs from CRDs for the handler.
	builtinCRDs := []*apiextensionsv1.CustomResourceDefinition{}
	for _, builtInAPI := range builtin.BuiltInAPIs {
		builtinCRDs = append(builtinCRDs, &apiextensionsv1.CustomResourceDefinition{
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: builtInAPI.GroupVersion.Group,
				Names: builtInAPI.Names,
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{
						Name:   builtInAPI.GroupVersion.Version,
						Served: true,
					},
				},
			},
		})
	}
	for _, crd := range builtinCRDs {
		gc.updateHandlers(&apiextensionsv1.CustomResourceDefinition{}, crd)
	}

	// A single crd handler will handle all dynamic resources.
	// The handler on the CRD informer manages the handlers that will
	// watch the resources across all clusters and feed changes into the
	// graph and deletion queue.
	//
	// TODO(ntnn): Might need a separate worker queue to not block and
	// requeue on error? But should be fine. This is just registering
	// a handler off of an informer so nothing should block or take
	// a long time. Technically there could be errors because both
	// .ForResource and .AddEventHandler _can_ return errors but both
	// never do.
	crdHandlers := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			gc.updateHandlers(
				&apiextensionsv1.CustomResourceDefinition{},
				tombstone.Obj[*apiextensionsv1.CustomResourceDefinition](obj),
			)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			gc.updateHandlers(
				tombstone.Obj[*apiextensionsv1.CustomResourceDefinition](oldObj),
				tombstone.Obj[*apiextensionsv1.CustomResourceDefinition](newObj),
			)
		},
		DeleteFunc: func(obj interface{}) {
			gc.updateHandlers(
				tombstone.Obj[*apiextensionsv1.CustomResourceDefinition](obj),
				&apiextensionsv1.CustomResourceDefinition{},
			)
		},
	}

	crdRegistration, err := gc.options.CRDInformer.Informer().AddEventHandler(crdHandlers)
	if err != nil {
		gc.log.Error(err, "error adding event handler for CRDs")
		return nil
	}
	return func() {
		if err := gc.options.CRDInformer.Informer().RemoveEventHandler(crdRegistration); err != nil {
			gc.log.Error(err, "error removing event handler for CRDs")
		}
	}
}

func (gc *GarbageCollector) updateHandlers(oldCrd, newCrd *apiextensionsv1.CustomResourceDefinition) {
	// Determine which versions should be present.
	newVersions := map[schema.GroupVersionResource]bool{}
	for _, version := range newCrd.Spec.Versions {
		gvr := schema.GroupVersionResource{
			Group:    newCrd.Spec.Group,
			Version:  version.Name,
			Resource: newCrd.Spec.Names.Plural,
		}
		newVersions[gvr] = true
	}

	// Determine which versions are being removed.
	oldVersions := map[schema.GroupVersionResource]bool{}
	for _, version := range oldCrd.Spec.Versions {
		gvr := schema.GroupVersionResource{
			Group:    oldCrd.Spec.Group,
			Version:  version.Name,
			Resource: oldCrd.Spec.Names.Plural,
		}
		if _, exists := newVersions[gvr]; !exists {
			// version only exists in old crd, mark for removal
			oldVersions[gvr] = true
		}
	}

	// Stop monitors for removed versions.
	for gvr := range oldVersions {
		gc.log.Info("stopping monitor for removed CRD version", "crd", oldCrd.Name, "gvr", gvr)
		cancel, ok := gc.handlerCancels[gvr]
		if !ok {
			// This shouldn't happen, events are guaranteed to be sent
			// in order so if an oldCrd with an unmonitored version
			// comes in something is probably wrong.
			gc.log.Error(nil, "no monitor found for CRD version to stop", "crd", oldCrd.Name, "gvr", gvr)
			continue
		}
		cancel()
	}

	// Start monitors for added versions.
	for gvr := range newVersions {
		gc.log.Info("ensuring monitor for CRD version", "crd", newCrd.Name, "gvr", gvr)
		if _, exists := gc.handlerCancels[gvr]; exists {
			// Skip if already monitored.
			continue
		}
		cancel, err := gc.registerHandlerForVersion(newCrd, gvr)
		if err != nil {
			gc.log.Error(err, "error starting monitor for CRD version", "crd", newCrd.Name, "gvr", gvr)
			continue
		}
		gc.handlerCancels[gvr] = cancel
	}
}

func (gc *GarbageCollector) registerHandlerForVersion(crd *apiextensionsv1.CustomResourceDefinition, gvr schema.GroupVersionResource) (func(), error) {
	gvk := schema.GroupVersionKind{
		Group:   gvr.Group,
		Version: gvr.Version,
		Kind:    crd.Spec.Names.Kind,
	}

	// Handler to add/update resources in the graph and to queue deletion.
	// Add and update directly updates the graph.
	// Deletion is queued to handle relationships and cascading.
	//
	// The graph add/update events are not queued into their own worker
	// queue because updating the graph cannot fail and is reasonably
	// fast.
	handlers := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			gc.log.Info("add event for object", "gvr", gvr, "obj", obj)
			u, or := gc.anyToRef(gvk, obj)
			owners := ObjectReferencesFromOwnerReferences(
				or.ClusterName,
				or.Namespace,
				u.GetOwnerReferences(),
			)
			gc.log.Info("adding object to graph", "object", or, "owners", owners)
			gc.graph.Add(or, nil, owners)
		},
		UpdateFunc: func(oldRaw, newRaw interface{}) {
			gc.log.Info("update event for object", "gvr", gvr, "oldObj", oldRaw, "newObj", newRaw)
			oldObj, oldRef := gc.anyToRef(gvk, oldRaw)
			oldOwners := ObjectReferencesFromOwnerReferences(
				oldRef.ClusterName,
				oldRef.Namespace,
				oldObj.GetOwnerReferences(),
			)

			newObj, newRef := gc.anyToRef(gvk, newRaw)
			newOwners := ObjectReferencesFromOwnerReferences(
				newRef.ClusterName,
				newRef.Namespace,
				newObj.GetOwnerReferences(),
			)

			gc.log.Info("updating object in graph", "object", newRef, "oldOwners", oldOwners, "newOwners", newOwners)
			gc.graph.Add(newRef, oldOwners, newOwners)
		},
		DeleteFunc: func(obj interface{}) {
			gc.log.Info("delete event for object", "gvr", gvr, "obj", obj)
			u, ref := gc.anyToRef(gvk, obj)
			gc.log.Info("queuing object for deletion", "object", ref)
			di := &deletionItem{
				reference:           ref,
				deletionPropagation: deletionPropagationFromFinalizers(u),
			}
			gc.deletionQueue.Add(di)
		},
	}

	// Neither ForResource nor AddEventHandler actually return errors.
	// We still handle them just in case that changes in the future.

	informer, err := gc.options.SharedInformerFactory.ForResource(gvr)
	if err != nil {
		return nil, fmt.Errorf("error getting informer for GVR %v: %w", gvr, err)
	}

	gc.log.Info("starting monitor for CRD version", "crd", crd.Name, "gvr", gvr)
	registration, err := informer.Informer().AddEventHandler(handlers)
	if err != nil {
		return nil, fmt.Errorf("error adding event handler for GVR %v: %w", gvr, err)
	}

	unregister := func() {
		if err := informer.Informer().RemoveEventHandler(registration); err != nil {
			gc.log.Error(err, "error removing event handler for CRD", "crd", crd.Name)
		}
	}

	return unregister, nil
}
