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

package nsmap

import (
	"errors"
	"os"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// ConfigMapName is the name of the configmap containing logical to physical namespace mappings
	ConfigMapName = "config-nsmap"
)

type OnUpdateFn func(configMap *corev1.ConfigMap)

func StartWatcher(done chan struct{}, callback OnUpdateFn) error {
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{},
		&clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return err
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}

	factory := informers.NewSharedInformerFactoryWithOptions(clientset, 0,
		informers.WithNamespace(os.Getenv("NAMESPACE")),
		informers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.FieldSelector = fields.OneTermEqualSelector(metav1.ObjectNameField, ConfigMapName).String()
		}))

	informer := factory.Core().V1().ConfigMaps().Informer()

	go informer.Run(done)

	if synced := cache.WaitForCacheSync(done, informer.HasSynced); !synced {
		return errors.New("configmap informer cache failed to sync")
	}

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			callback(obj.(*corev1.ConfigMap))
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			callback(newObj.(*corev1.ConfigMap))
		},
		DeleteFunc: func(obj interface{}) {
			callback(nil)
		},
	})

	return nil
}
