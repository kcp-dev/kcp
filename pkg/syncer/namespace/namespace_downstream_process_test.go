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

package namespace

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/client"
)

func TestSyncerNamespaceProcess(t *testing.T) {
	tests := map[string]struct {
		upstreamNamespaceExists bool
		deletedNamespace        string
		syncTargetUID           types.UID

		upstreamNamespaceExistsError                    error
		getDownstreamNamespaceError                     error
		getDownstreamNamespaceFromNamespaceLocatorError error

		eventOrigin string // upstream or downstream
	}{
		"NamespaceSyncer removes downstream namespace when no matching upstream has been found, expect downstream namespace deletion": {
			upstreamNamespaceExists: false,
			deletedNamespace:        "kcp-hcbsa8z6c2er",
			eventOrigin:             "downstream",
		},
		"NamespaceSyncer doesn't remove downstream namespace when nsLocator synctarget UID is different, expect no namespace deletion": {
			upstreamNamespaceExists: false,
			deletedNamespace:        "",
			eventOrigin:             "downstream",
			syncTargetUID:           "1234",
		},
		"NamespaceSyncer, downstream event, no deletion as there is a matching upstream namespace, expect no namespace deletion": {
			upstreamNamespaceExists: true,
			deletedNamespace:        "",
			eventOrigin:             "downstream",
		},
		"NamespaceSyncer, downstream event, error trying to get the upstream namespace, expect no namespace deletion": {
			upstreamNamespaceExistsError: errors.New("error"),
			deletedNamespace:             "",
			eventOrigin:                  "downstream",
		},
		"NamespaceSyncer, downstream event, error trying to get the downstream namespace, expect no namespace deletion": {
			getDownstreamNamespaceError: errors.New("error"),
			deletedNamespace:            "",
			eventOrigin:                 "downstream",
		},
		"NamespaceSyncer, downstream event, downstream namespace is not found, expect no namespace deletion": {
			getDownstreamNamespaceError: apierrors.NewNotFound(schema.GroupResource(metav1.GroupResource{Group: "", Resource: ""}), "not-found"),
			deletedNamespace:            "",
			eventOrigin:                 "downstream",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			downstreamNamespace := namespace(logicalcluster.New(""), "kcp-hcbsa8z6c2er", map[string]string{
				"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
			}, map[string]string{
				"kcp.dev/namespace-locator": `{"syncTarget":{"workspace":"root:org:ws","name":"us-west1","uid":"syncTargetUID"},"workspace":"root:org:ws","namespace":"test"}`,
			})
			syncTargetClusterName := logicalcluster.Name("root:org:ws")
			syncTargetName := "us-west1"
			syncTargetKey := workloadv1alpha1.ToSyncTargetKey(syncTargetClusterName, syncTargetName)
			syncTargetUID := types.UID("syncTargetUID")
			if tc.syncTargetUID != "" {
				syncTargetUID = tc.syncTargetUID
			}
			nsController := DownstreamController{
				toDeleteMap:  make(map[string]time.Time),
				delayedQueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), downstreamControllerName),
				deleteDownstreamNamespace: func(ctx context.Context, downstreamNamespaceName string) error {
					return nil
				},
				upstreamNamespaceExists: func(clusterName logicalcluster.Name, upstreamNamespaceName string) (bool, error) {
					return tc.upstreamNamespaceExists, tc.upstreamNamespaceExistsError
				},
				getDownstreamNamespace: func(name string) (runtime.Object, error) {
					nsJSON, _ := json.Marshal(downstreamNamespace)
					unstructured := &unstructured.Unstructured{}
					_ = json.Unmarshal(nsJSON, unstructured)
					return unstructured, tc.getDownstreamNamespaceError
				},
				listDownstreamNamespaces: func() (ret []runtime.Object, err error) {
					return []runtime.Object{}, nil
				},
				createConfigMap: func(ctx context.Context, configMap *corev1.ConfigMap) (*corev1.ConfigMap, error) {
					return nil, nil
				},
				updateConfigMap: func(ctx context.Context, configMap *corev1.ConfigMap) (*corev1.ConfigMap, error) {
					return nil, nil
				},
				isDowntreamNamespaceEmpty: func(ctx context.Context, namespaceName string) (bool, error) {
					return true, nil
				},
				syncTargetName:        syncTargetName,
				syncTargetClusterName: syncTargetClusterName,
				syncTargetUID:         syncTargetUID,
				syncTargetKey:         syncTargetKey,
				dnsNamespace:          "kcp-hcbsa8z6c2er",
			}

			var key string
			if tc.eventOrigin == "downstream" {
				key = downstreamNamespace.GetName()
			} else if tc.eventOrigin == "upstream" {
				key = client.ToClusterAwareKey(logicalcluster.New("root:org:ws"), "test")
			} else {
				t.Fatalf("unexpected event origin: %s", tc.eventOrigin)
			}

			err := nsController.process(ctx, key)
			require.NoError(t, err)

			if tc.deletedNamespace != "" {
				require.True(t, nsController.isPlannedForCleaning(tc.deletedNamespace))
				require.Equal(t, len(nsController.toDeleteMap), 1)
			} else {
				require.Empty(t, len(nsController.toDeleteMap))
			}
		})
	}
}

func namespace(clusterName logicalcluster.Path, name string, labels, annotations map[string]string) *corev1.Namespace {
	if !clusterName.Empty() {
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations[logicalcluster.AnnotationKey] = clusterName.String()
	}

	return &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Labels:      labels,
			Annotations: annotations,
		},
	}
}
