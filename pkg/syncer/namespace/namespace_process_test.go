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

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clusters"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

func TestSyncerNamespaceProcess(t *testing.T) {
	tests := map[string]struct {
		upstreamNamespaceExists bool
		deletedNamespace        string

		upstreamNamespaceExistsError                    error
		getDownstreamNamespaceError                     error
		getDownstreamNamespaceFromNamespaceLocatorError error

		eventOrigin string // upstream or downstream
	}{
		"NamespaceSyncer remove downstream namespace when upstream namespace has been deleted, expect downstream namespace deletion": {
			upstreamNamespaceExists: false,
			deletedNamespace:        "kcp-hcbsa8z6c2er",
			eventOrigin:             "upstream",
		},
		"NamespaceSyncer removes downstream namespace when no matching upstream has been found, expect downstream namespace deletion": {
			upstreamNamespaceExists: false,
			deletedNamespace:        "kcp-hcbsa8z6c2er",
			eventOrigin:             "downstream",
		},
		"NamespaceSyncer, downstream event, no deletion as there is a matching upstream namespace, expect no namespace deletion": {
			upstreamNamespaceExists: true,
			deletedNamespace:        "",
			eventOrigin:             "downstream",
		},
		"NamespaceSyncer, upstream event, no deletion as there is a matching upstream namespace, expect no namespace deletion": {
			upstreamNamespaceExists: true,
			deletedNamespace:        "",
			eventOrigin:             "upstream",
		},
		"NamespaceSyncer, downstream event, error trying to get the upstream namespace, expect no namespace deletion": {
			upstreamNamespaceExistsError: errors.New("error"),
			deletedNamespace:             "",
			eventOrigin:                  "downstream",
		},
		"NamespaceSyncer, upstream event, error trying to get the upstream namespace, expect no namespace deletion": {
			upstreamNamespaceExistsError: errors.New("error"),
			deletedNamespace:             "",
			eventOrigin:                  "upstream",
		},
		"NamespaceSyncer, downstream event, error trying to get the downstream namespace, expect no namespace deletion": {
			getDownstreamNamespaceError: errors.New("error"),
			deletedNamespace:            "",
			eventOrigin:                 "downstream",
		},
		"NamespaceSyncer, upstream event, error trying to get the downstream namespace, expect no namespace deletion": {
			getDownstreamNamespaceError:                     errors.New("error"),
			getDownstreamNamespaceFromNamespaceLocatorError: errors.New("error"),
			deletedNamespace:                                "",
			eventOrigin:                                     "upstream",
		},
		"NamespaceSyncer, downstream event, downstream namespace is not found, expect no namespace deletion": {
			getDownstreamNamespaceError: apierrors.NewNotFound(schema.GroupResource(metav1.GroupResource{Group: "", Resource: ""}), "not-found"),
			deletedNamespace:            "",
			eventOrigin:                 "downstream",
		},
		"NamespaceSyncer, upstream event, downstream namespace is not found, expect no namespace deletion": {
			getDownstreamNamespaceError:                     apierrors.NewNotFound(schema.GroupResource(metav1.GroupResource{Group: "", Resource: ""}), "not-found"),
			getDownstreamNamespaceFromNamespaceLocatorError: apierrors.NewNotFound(schema.GroupResource(metav1.GroupResource{Group: "", Resource: ""}), "not-found"),
			deletedNamespace:                                "",
			eventOrigin:                                     "upstream",
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
			syncTargetWorkspace := logicalcluster.New("root:org:ws")
			syncTargetName := "us-west1"
			syncTargetKey := workloadv1alpha1.ToSyncTargetKey(syncTargetWorkspace, syncTargetName)
			deletedNamespace := ""

			nsController := Controller{
				deleteDownstreamNamespace: func(ctx context.Context, downstreamNamespaceName string) error {
					deletedNamespace = downstreamNamespaceName
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
				getDownstreamNamespaceFromNamespaceLocator: func(namespaceLocator shared.NamespaceLocator) (runtime.Object, error) {
					nsJSON, _ := json.Marshal(downstreamNamespace)
					unstructured := &unstructured.Unstructured{}
					_ = json.Unmarshal(nsJSON, unstructured)
					return unstructured, tc.getDownstreamNamespaceFromNamespaceLocatorError
				},
				syncTargetName:      syncTargetName,
				syncTargetWorkspace: syncTargetWorkspace,
				syncTargetUID:       types.UID("syncTargetUID"),
				syncTargetKey:       syncTargetKey,
			}

			var key string
			if tc.eventOrigin == "downstream" {
				key = downstreamNamespace.GetName()
			} else if tc.eventOrigin == "upstream" {
				key = clusters.ToClusterAwareKey(logicalcluster.New("root:org:ws"), "test")
			} else {
				t.Fatalf("unexpected event origin: %s", tc.eventOrigin)
			}

			err := nsController.process(ctx, key)
			require.NoError(t, err)
			require.Equal(t, tc.deletedNamespace, deletedNamespace)
		})
	}
}

func namespace(clusterName logicalcluster.Name, name string, labels, annotations map[string]string) *corev1.Namespace {
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
