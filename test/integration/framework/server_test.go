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

package framework

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/kcp/sdk/apis/core"
)

func TestServer(t *testing.T) {
	StartTestServer(t)
}

func TestServerCreateConfigMap(t *testing.T) {
	_, kubeClient, _ := StartTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	configmap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: metav1.NamespaceDefault,
		},
		Data: map[string]string{
			"foo": "bar",
		},
	}

	_, err := kubeClient.Cluster(core.RootCluster.Path()).
		CoreV1().
		ConfigMaps(metav1.NamespaceDefault).
		Create(ctx, configmap, metav1.CreateOptions{})
	require.Nil(t, err)
}
