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
	"encoding/json"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type getFinalizers interface {
	GetFinalizers() []string
}

func hasFinalizer(obj getFinalizers, finalizer string) bool {
	finalizers := obj.GetFinalizers()
	return slices.Contains(finalizers, finalizer)
}

func patchRemoveFinalizer(obj getFinalizers, finalizer string) ([]byte, error) {
	finalizers := obj.GetFinalizers()
	newFinalizers := slices.Delete(finalizers, slices.Index(finalizers, finalizer), slices.Index(finalizers, finalizer)+1)
	dummy := metav1.PartialObjectMetadata{
		ObjectMeta: metav1.ObjectMeta{
			Finalizers: newFinalizers,
		},
	}
	return json.Marshal(&dummy)
}

type getOwnerReferences interface {
	GetOwnerReferences() []metav1.OwnerReference
}

func patchRemoveOwnerReference(obj getOwnerReferences, ownerReferenceUID types.UID) ([]byte, error) {
	ownerReferences := obj.GetOwnerReferences()
	newOwnerReferences := slices.DeleteFunc(ownerReferences, func(ref metav1.OwnerReference) bool {
		return ref.UID == ownerReferenceUID
	})
	dummy := metav1.PartialObjectMetadata{
		ObjectMeta: metav1.ObjectMeta{
			OwnerReferences: newOwnerReferences,
		},
	}
	return json.Marshal(&dummy)
}

func deletionPropagationFromFinalizers(obj getFinalizers) metav1.DeletionPropagation {
	finalizers := obj.GetFinalizers()
	switch {
	case slices.Contains(finalizers, metav1.FinalizerOrphanDependents):
		return metav1.DeletePropagationOrphan
	case slices.Contains(finalizers, metav1.FinalizerDeleteDependents):
		return metav1.DeletePropagationForeground
	default:
		return metav1.DeletePropagationBackground
	}
}
