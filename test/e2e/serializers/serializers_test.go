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

package serializers

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"

	"github.com/kcp-dev/kcp/sdk/apis/core"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

var serializers = []string{
	runtime.ContentTypeJSON,
	runtime.ContentTypeYAML,
	runtime.ContentTypeProtobuf,
	runtime.ContentTypeCBOR,
	// cbor-seq is only used for streaming cbor, not for normal requests
	// runtime.ContentTypeCBORSequence,
}

func TestSerializers(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	for _, serializer := range serializers {
		t.Run(serializer, func(t *testing.T) {
			t.Parallel()

			cfg := server.BaseConfig(t)
			cfg.ContentConfig.AcceptContentTypes = serializer
			cfg.ContentConfig.ContentType = serializer

			kubeClient, err := kcpkubernetesclientset.NewForConfig(cfg)
			require.NoError(t, err, "error creating kube cluster client")

			configmap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      strings.TrimPrefix(serializer, "application/"),
					Namespace: metav1.NamespaceDefault,
				},
				Data: map[string]string{
					"serializer": serializer,
				},
			}

			cmi := kubeClient.Cluster(core.RootCluster.Path()).
				CoreV1().
				ConfigMaps(metav1.NamespaceDefault)

			_, err = cmi.Create(context.Background(), configmap, metav1.CreateOptions{})
			require.Nil(t, err)

			cm, err := cmi.Get(context.Background(), configmap.ObjectMeta.Name, metav1.GetOptions{})
			require.Nil(t, err)
			require.NotNil(t, cm)
			require.Equal(t, configmap.Data, cm.Data)

			err = cmi.Delete(context.Background(), configmap.ObjectMeta.Name, metav1.DeleteOptions{})
			require.Nil(t, err)
		})
	}
}
