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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/kcp/pkg/tombstone"
)

func (gc *GarbageCollector) anyToRef(gvk schema.GroupVersionKind, obj any) (*unstructured.Unstructured, ObjectReference) {
	// TODO(ntnn): sometimes we get unstructured, sometimes partial metadata
	u := tombstone.Obj[*unstructured.Unstructured](obj)
	ref := ObjectReferenceFrom(u)

	switch {
	case u.GetKind() == "PartialObjectMetadata":
		// Override wrong info from PartialObjectMetadata
		ref.APIVersion = gvk.GroupVersion().String()
		ref.Kind = gvk.Kind
	}

	return u, ref
}

func (gc *GarbageCollector) GVR(or ObjectReference) (schema.GroupVersionResource, error) {
	gvk := schema.FromAPIVersionAndKind(or.OwnerReference.APIVersion, or.OwnerReference.Kind)
	forCluster := gc.options.DynRESTMapper.ForCluster(or.ClusterName)
	mapping, err := forCluster.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return schema.GroupVersionResource{}, err
	}
	return mapping.Resource, nil
}
