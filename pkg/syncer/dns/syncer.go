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

package dns

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/dns/plugin/nsmap"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

// StartNsMapSyncer watches for namespace changes scoped by the given informer factory
// and update the dns nsmap config map
func StartNsMapSyncer(ctx context.Context, client *dynamic.DynamicClient, informers dynamicinformer.DynamicSharedInformerFactory, ns string) {
	nsinformer := informers.ForResource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})
	nslister := nsinformer.Lister()

	cmclient := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).Namespace(ns)

	nsinformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			updateConfigMap(ctx, cmclient, nslister, ns)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			updateConfigMap(ctx, cmclient, nslister, ns)
		},
		DeleteFunc: func(obj interface{}) {
			updateConfigMap(ctx, cmclient, nslister, ns)
		},
	})
}

func updateConfigMap(ctx context.Context, client dynamic.ResourceInterface, nslister cache.GenericLister, namespace string) {
	fmt.Println("update configmap")

	objs, err := nslister.List(labels.Everything()) // filtering the done at the informer level
	if err != nil {
		klog.Warningf("failed to update %s (%v)", nsmap.ConfigMapName, err)
		return
	}

	data := make(map[string]string)

	for _, obj := range objs {
		ns := obj.(*unstructured.Unstructured)
		annotations := ns.GetAnnotations()
		if annotations == nil {
			// skip
			continue
		}

		locator, found, err := shared.LocatorFromAnnotations(annotations)
		if err != nil {
			// Corrupted ns locator annotation value
			klog.Warningf("invalid namespace locator %s (%w)", ns.GetName(), err)
			continue
		}

		if !found {
			continue
		}

		data[locator.Namespace] = ns.GetName()
	}

	cm := corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Name:      nsmap.ConfigMapName,
			Namespace: namespace,
		},
		Data: data,
	}

	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&cm)
	if err != nil {
		klog.Warningf("failed to convert configmap: %v", err)
		return
	}

	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		_, err := client.Update(ctx, &unstructured.Unstructured{Object: obj}, metav1.UpdateOptions{})

		if apierrs.IsNotFound(err) {
			_, err = client.Create(ctx, &unstructured.Unstructured{Object: obj}, metav1.CreateOptions{})
		}
		return err
	})

	if err != nil {
		klog.Warningf("failed to update the nsmap configmap: %v", err)
	}
}
