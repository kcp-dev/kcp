package virtualresources

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// DummyStorage implements rest.Storage, rest.Lister, rest.Getter
type DummyStorage struct {
	gvr schema.GroupVersionResource
}

func NewDummyStorage(gvr schema.GroupVersionResource) *DummyStorage {
	return &DummyStorage{gvr: gvr}
}

func (d *DummyStorage) Destroy() {
	// No-op cleanup; add logic here if needed
	fmt.Println("Destroy called on DummyStorage for", d.gvr.Resource)
}

func (d *DummyStorage) NamespaceScoped() bool {
	return true
}

func (d *DummyStorage) New() runtime.Object {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(d.gvr.GroupVersion().WithKind("Cowboy"))
	return obj
}

func (d *DummyStorage) NewList() runtime.Object {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(d.gvr.GroupVersion().WithKind("CowboyList"))
	return list
}

// Get returns a dummy object with the requested name
func (d *DummyStorage) Get(ctx context.Context, name string, opts *metav1.GetOptions) (runtime.Object, error) {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": d.gvr.GroupVersion().String(),
			"kind":       "Cowboy",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": "default",
			},
			"dummyField": "hello from Get",
		},
	}
	obj.SetGroupVersionKind(d.gvr.GroupVersion().WithKind("Cowboy"))
	return obj, nil
}

// List returns a list with a single dummy object
func (d *DummyStorage) List(ctx context.Context, opts *metav1.ListOptions) (runtime.Object, error) {
	item := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": d.gvr.GroupVersion().String(),
			"kind":       "Cowboy",
			"metadata": map[string]interface{}{
				"name":      "example",
				"namespace": "default",
			},
			"dummyField": "hello from List",
		},
	}
	item.SetGroupVersionKind(d.gvr.GroupVersion().WithKind("Cowboy"))
	list := &unstructured.UnstructuredList{
		Object: map[string]interface{}{
			"apiVersion": d.gvr.GroupVersion().String(),
			"kind":       "CowboyList",
		},
		Items: []unstructured.Unstructured{*item},
	}
	item.SetGroupVersionKind(d.gvr.GroupVersion().WithKind("CowboyList"))
	return list, nil
}
