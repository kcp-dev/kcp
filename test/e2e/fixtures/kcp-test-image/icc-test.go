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
	"errors"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt)
	configMapName := os.Getenv("CONFIGMAP_NAME")

	if configMapName == "" {
		log.Panicln("ENV variable CONFIGMAP_NAME not specified")
	}

	namespace, err := DetectNamespace()
	if err != nil {
		log.Panicln("no namespace detected", err)
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		log.Panicln("failed to create in-cluster config", err)
	}

	clientset, err := kubernetesclient.NewForConfig(config)
	if err != nil {
		log.Panicln("failed to create clientset with in-cluster config", config, err)
	}

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: configMapName,
		},
	}
	_, err = clientset.CoreV1().ConfigMaps(namespace).Create(ctx, configMap, metav1.CreateOptions{})
	if err != nil {
		log.Panicln("failed to create configmap", err)
	}

	log.Printf("configmap %s created. Going to sleep.\n", configMapName)

	<-ctx.Done()
}

func DetectNamespace() (string, error) {
	if namespaceData, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		if namespace := strings.TrimSpace(string(namespaceData)); len(namespace) > 0 {
			return namespace, nil
		}
		return "", err
	}
	return "", errors.New("failed to detect in-cluster namespace")
}
