/*
Copyright 2022 The Kube Bind Authors.

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

package resources

import (
	"context"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	IdentityAnnotationKey = "example-backend.kube-bind.io/identity"
)

func CreateNamespace(ctx context.Context, client kubernetes.Interface, generateName, id string) (*corev1.Namespace, error) {
	if !strings.HasSuffix(generateName, "-") {
		generateName = generateName + "-"
	}
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: generateName,
			Annotations: map[string]string{
				IdentityAnnotationKey: id,
			},
		},
	}

	ns, err := client.CoreV1().Namespaces().Create(ctx, namespace, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return nil, err
	} else if errors.IsAlreadyExists(err) {
		ns, err := client.CoreV1().Namespaces().Get(ctx, namespace.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if ns.Annotations[IdentityAnnotationKey] != id {
			return nil, errors.NewAlreadyExists(corev1.Resource("namespace"), ns.Name)
		}
	}

	return ns, err
}
