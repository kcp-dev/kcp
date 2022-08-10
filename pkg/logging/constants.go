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

// Package logging supplies common constants to ensure consistent use of structured logs.
package logging

import (
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	"github.com/kcp-dev/logicalcluster/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	// ReconcilerKey is used to identify a reconciler.
	ReconcilerKey = "reconciler"

	// QueueKeyKey is used to expose the workqueue key being processed.
	QueueKeyKey = "key"

	// WorkspaceKey is used to specify a workspace when a log is related to an object.
	WorkspaceKey = "workspace"
	// NamespaceKey is used to specify a namespace when a log is related to an object.
	NamespaceKey = "namespace"
	// NameKey is used to specify a name when a log is related to an object.
	NameKey = "name"
	// APIVersionKey is used to specify an API version when a log is related to an object.
	APIVersionKey = "apiVersion"
)

// WithReconciler adds the reconciler name to the logger.
func WithReconciler(logger logr.Logger, reconciler string) logr.Logger {
	return logger.WithValues(ReconcilerKey, reconciler)
}

// WithQueueKey adds the queue key to the logger.
func WithQueueKey(logger logr.Logger, key string) logr.Logger {
	return logger.WithValues(QueueKeyKey, key)
}

type Object interface {
	metav1.Object
	runtime.Object
}

// WithObject adds object identifiers to the logger.
func WithObject(logger logr.Logger, obj Object) logr.Logger {
	return logger.WithValues(From(obj)...)
}

// From provides the structured logging fields that identify an object, prefixing with the resource name.
func From(obj Object) []interface{} {
	gvk := obj.GetObjectKind().GroupVersionKind()
	kind := gvk.Kind
	if kind == "" {
		// if there's no Kind present on the object, use the Go type name, without any package prefix
		objType := fmt.Sprintf("%T", obj)
		kind = objType[strings.Index(objType, ".")+1:]
	}
	prefix := strings.ToLower(kind)
	return FromPrefix(prefix, obj)
}

// FromPrefix provides the structured logging fields that identify an object, allowing any prefix.
func FromPrefix(prefix string, obj Object) []interface{} {
	gvk := obj.GetObjectKind().GroupVersionKind()
	return []interface{}{
		prefix + "." + WorkspaceKey,
		logicalcluster.From(obj).String(),
		prefix + "." + NamespaceKey,
		obj.GetNamespace(),
		prefix + "." + NameKey,
		obj.GetName(),
		prefix + "." + APIVersionKey,
		gvk.GroupVersion(),
	}
}
