/*
Copyright 2026 The kcp Authors.

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

// Package clusterannotation provides helpers to stamp the authoritative
// logicalcluster.AnnotationKey (kcp.io/cluster) annotation onto objects before
// they are handed to provider-defined admission webhooks and admission policies.
//
// kcp documents that objects passed to a provider's webhook or admission policy
// carry an authoritative kcp.io/cluster annotation naming the workspace that
// owns the object, so a provider can distinguish one consumer tenant from
// another. The value must therefore reflect the logical cluster of the request,
// not whatever the client happened to send in the object body.
//
// The storage layer stamps the persisted value on both CREATE and UPDATE, so
// the stored object is always authoritative. However, admission runs before the
// storage layer, so admission hooks observe the client-supplied body unless the
// value is stamped first. Stamping must therefore happen for UPDATE as well as
// CREATE; otherwise a consumer that may only update an object in its own
// workspace can make a provider's hook attribute the object to a different
// tenant.
package clusterannotation

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"

	"github.com/kcp-dev/logicalcluster/v3"
)

// Stamp forces the authoritative kcp.io/cluster annotation onto the object being
// admitted so that provider-defined admission hooks and policies always observe
// the owning logical cluster rather than a client-supplied value. It applies to
// CREATE and UPDATE (the two operations that carry a new object); other
// operations are left untouched.
//
// It returns an undo function that reverts the change after admission (may be
// nil when there is nothing to revert). The persisted value is stamped
// independently by the storage layer, so reverting here does not change what is
// stored.
func Stamp(attr admission.Attributes, clusterName logicalcluster.Name) func() {
	switch attr.GetOperation() {
	case admission.Create, admission.Update:
	default:
		return nil
	}

	obj, ok := attr.GetObject().(metav1.Object)
	if !ok {
		return nil
	}

	return setClusterAnnotation(obj, clusterName)
}

// setClusterAnnotation sets the cluster annotation on the given object to the
// given clusterName, returning an undo function that reverts the change, or nil
// if the annotation was already set to clusterName (nothing to undo).
func setClusterAnnotation(obj metav1.Object, clusterName logicalcluster.Name) func() {
	undoFn := func() {
		anns := obj.GetAnnotations()
		delete(anns, logicalcluster.AnnotationKey)
		obj.SetAnnotations(anns)
	}

	anns := obj.GetAnnotations()
	if anns == nil {
		obj.SetAnnotations(map[string]string{logicalcluster.AnnotationKey: clusterName.String()})
		return undoFn
	}

	old, ok := anns[logicalcluster.AnnotationKey]
	if ok && old == clusterName.String() {
		return nil
	}

	anns[logicalcluster.AnnotationKey] = clusterName.String()
	obj.SetAnnotations(anns)
	return undoFn
}
