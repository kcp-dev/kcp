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

package garbagecollector

import (
	"fmt"

	"github.com/go-logr/logr"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kcp-dev/logicalcluster/v3"
)

// ID is a globally (meaning across logical clusters) unique identifier
// for an object.
type ID struct {
	ClusterName logicalcluster.Name
	UID         types.UID
}

// ObjectReference is a reference to an object in a specific logical cluster.
type ObjectReference struct {
	metav1.OwnerReference
	Namespace   string
	ClusterName logicalcluster.Name
}

func ObjectReferenceFrom(obj *unstructured.Unstructured) ObjectReference {
	or := ObjectReference{}

	or.APIVersion = obj.GetObjectKind().GroupVersionKind().GroupVersion().String()
	or.Kind = obj.GetObjectKind().GroupVersionKind().Kind
	or.Name = obj.GetName()
	or.UID = obj.GetUID()

	// TODO(ntnn): handle BlockOwnerDeletion
	// or.BlockOwnerDeletion =

	or.Namespace = obj.GetNamespace()
	or.ClusterName = logicalcluster.From(obj)

	return or
}

func ObjectReferenceFromOwnerReference(clusterName logicalcluster.Name, namespace string, ownerRef metav1.OwnerReference) ObjectReference {
	return ObjectReference{
		OwnerReference: ownerRef,
		Namespace:      namespace,
		ClusterName:    clusterName,
	}
}

func ObjectReferencesFromOwnerReferences(clusterName logicalcluster.Name, namespace string, ownerRefs []metav1.OwnerReference) []ObjectReference {
	refs := make([]ObjectReference, 0, len(ownerRefs))
	for _, ownerRef := range ownerRefs {
		refs = append(refs, ObjectReferenceFromOwnerReference(clusterName, namespace, ownerRef))
	}
	return refs
}

// String is used when logging an ObjectReference in text format.
func (s ObjectReference) String() string {
	return fmt.Sprintf("[%s %s/%s, namespace: %s, name: %s, uid: %s]", s.ClusterName.String(), s.APIVersion, s.Kind, s.Namespace, s.Name, s.UID)
}

var _ fmt.Stringer = ObjectReference{}

// ID returns a globally unique identifier for the ObjectReference.
func (s ObjectReference) ID() ID {
	return ID{UID: s.UID, ClusterName: s.ClusterName}
}

// MarshalLog is used when logging an ObjectReference in JSON format.
func (s ObjectReference) MarshalLog() interface{} {
	return struct {
		ClusterName logicalcluster.Name `json:"clusterName"`
		APIVersion  string              `json:"apiVersion"`
		Kind        string              `json:"kind"`
		Namespace   string              `json:"namespace"`
		Name        string              `json:"name"`
		UID         types.UID           `json:"uid"`
	}{
		ClusterName: s.ClusterName,
		APIVersion:  s.APIVersion,
		Kind:        s.Kind,
		Namespace:   s.Namespace,
		Name:        s.Name,
		UID:         s.UID,
	}
}

var _ logr.Marshaler = ObjectReference{}

func (s ObjectReference) Equals(other ObjectReference) bool {
	return s.ClusterName == other.ClusterName &&
		s.APIVersion == other.APIVersion &&
		s.Kind == other.Kind &&
		s.Namespace == other.Namespace &&
		s.Name == other.Name &&
		s.UID == other.UID
}
