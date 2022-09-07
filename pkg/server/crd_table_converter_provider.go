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

package server

import (
	"context"
	"reflect"

	"k8s.io/apiextensions-apiserver/pkg/apiserver"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/kubernetes/pkg/api/legacyscheme"
	"k8s.io/kubernetes/pkg/printers"
	printersinternal "k8s.io/kubernetes/pkg/printers/internalversion"
	printerstorage "k8s.io/kubernetes/pkg/printers/storage"
)

type TableConverterFunc func(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error)

func (tcf TableConverterFunc) ConvertToTable(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
	return tcf(ctx, object, tableOptions)
}

type tableConverterProvider struct {
	internalTableConverter printerstorage.TableConvertor
}

var _ apiserver.TableConverterProvider = &tableConverterProvider{}

func NewTableConverterProvider() *tableConverterProvider {
	return &tableConverterProvider{
		internalTableConverter: printerstorage.TableConvertor{
			TableGenerator: printers.NewTableGenerator().With(printersinternal.AddHandlers),
		},
	}
}

// GetTableConverter replaces the table converter of CRDs for built-in Kubernetes resources with the
// default table converter implementation for the built-in type from Kubernetes. Currently CRDs only
// allow defining custom table column based on a single basic JsonPath expression. This is not
// sufficient to reproduce the various column definitions of built-in Kubernetes resources such as
// deployments, as those definitions are implemented in Go code. In kcp, when deployments are served
// via a CRD, the table columns shown from a `kubectl get deployments` command are not the ones
// typically expected.
//
// In the future this should probably be replaced by some new mechanism that would allow customizing
// some behaviors of resources defined by CRDs.
func (t *tableConverterProvider) GetTableConverter(group, kind, listKind string) rest.TableConvertor {
	objectGVK := schema.GroupVersionKind{
		Group:   group,
		Kind:    kind,
		Version: runtime.APIVersionInternal,
	}
	objectType, objectTypeExists := legacyscheme.Scheme.AllKnownTypes()[objectGVK]

	listGVK := schema.GroupVersionKind{
		Group:   group,
		Kind:    listKind,
		Version: runtime.APIVersionInternal,
	}
	listType, listTypeExists := legacyscheme.Scheme.AllKnownTypes()[listGVK]

	if !objectTypeExists || !listTypeExists {
		return nil
	}

	return TableConverterFunc(func(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
		k := object.GetObjectKind().GroupVersionKind()

		var theType reflect.Type
		switch k.Kind {
		case kind:
			theType = objectType
		default:
			theType = listType
		}

		out := reflect.New(theType).Interface().(runtime.Object)

		if err := legacyscheme.Scheme.Convert(object, out, nil); err != nil {
			return nil, err
		}

		return t.internalTableConverter.ConvertToTable(ctx, out, tableOptions)
	})
}
