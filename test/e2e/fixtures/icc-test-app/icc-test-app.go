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

package main

import (
	"context"
	"log"
	"os"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	namespaceName := os.Getenv("TARGET_NAMESPACE")
	configMapName := os.Getenv("CONFIGMAP_NAME")

	if namespaceName == "" || configMapName == "" {
		log.Panicln("ENV variables TARGET_NAMESPACE and CONFIGMAP_NAME not specified")
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		log.Panicln("failed to create in-cluster config", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Panicln("failed to create clientset with in-cluster config", config, err)
	}

	configMap := createConfigMap(namespaceName, configMapName)
	_, err = clientset.CoreV1().ConfigMaps(namespaceName).Create(context.TODO(), configMap, metav1.CreateOptions{})
	if err != nil {
		log.Panicln("failed to create configmap", err)
	}

	log.Printf("configmap %s created in namespace %s\n", namespaceName, configMap)
}

func createConfigMap(namespace string, name string) *corev1.ConfigMap {
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
	}
	return configMap

}
