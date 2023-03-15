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

package endpoints

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	workloadv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
)

var scheme *runtime.Scheme

func init() {
	scheme = runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
}

func TestEndpointsControllerProcess(t *testing.T) {
	defaultSyncTargetLocator := shared.SyncTargetLocator{
		ClusterName: "root:org:ws",
		Name:        "us-west1",
		UID:         types.UID("syncTargetUID"),
	}

	tests := map[string]struct {
		endpointName string

		syncTargetLocator *shared.SyncTargetLocator

		downstreamNamespace *corev1.Namespace
		namespaceGetError   error

		downstreamEndpoints *corev1.Endpoints
		endpointsGetError   error

		downstreamService *corev1.Service
		serviceGetError   error

		expectedError     string
		expectedPatch     string
		expectedPatchType types.PatchType
	}{
		"Label the endpoints when service is there and annotated": {
			endpointName: "httpecho",
			downstreamNamespace: namespace("downstream-ns").WithLocator(t, shared.NamespaceLocator{SyncTarget: defaultSyncTargetLocator,
				ClusterName: logicalcluster.Name("root:org:ws"),
				Namespace:   "ns",
			}).Object(),
			downstreamEndpoints: endpoints("httpecho").WithNamespace("downstream-ns").Object(),
			downstreamService: service("httpecho").WithNamespace("downstream-ns").WithAnnotations(map[string]string{
				"experimental.workload.kcp.io/upsync-derived-resources": "pods,endpoints",
			}).Object(),
			expectedPatch:     `{"metadata": {"labels": {"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g":"Upsync"}}}`,
			expectedPatchType: types.StrategicMergePatchType,
		},
		"Don't label the endpoints when service is there but not annotated correctly": {
			endpointName: "httpecho",
			downstreamNamespace: namespace("downstream-ns").WithLocator(t, shared.NamespaceLocator{SyncTarget: defaultSyncTargetLocator,
				ClusterName: logicalcluster.Name("root:org:ws"),
				Namespace:   "ns",
			}).Object(),
			downstreamEndpoints: endpoints("httpecho").WithNamespace("downstream-ns").Object(),
			downstreamService: service("httpecho").WithNamespace("downstream-ns").WithAnnotations(map[string]string{
				"workload.kcp.io/upsync-derived-resources": "pods",
			}).Object(),
		},
		"Don't label the endpoints when service is there but not annotated": {
			endpointName: "httpecho",
			downstreamNamespace: namespace("downstream-ns").WithLocator(t, shared.NamespaceLocator{SyncTarget: defaultSyncTargetLocator,
				ClusterName: logicalcluster.Name("root:org:ws"),
				Namespace:   "ns",
			}).Object(),
			downstreamEndpoints: endpoints("httpecho").WithNamespace("downstream-ns").Object(),
			downstreamService:   service("httpecho").WithNamespace("downstream-ns").Object(),
		},
		"Don't label the endpoints when namespace resource is not found": {
			endpointName:        "httpecho",
			downstreamEndpoints: endpoints("httpecho").WithNamespace("downstream-ns").Object(),
			downstreamService:   service("httpecho").WithNamespace("downstream-ns").Object(),
		},
		"Error when namespace retrieval fails": {
			endpointName:        "httpecho",
			namespaceGetError:   errors.New("error"),
			downstreamEndpoints: endpoints("httpecho").WithNamespace("downstream-ns").Object(),
			downstreamService:   service("httpecho").WithNamespace("downstream-ns").Object(),
			expectedError:       "error",
		},
		"Don't label the endpoints when namespace has no locator": {
			endpointName:        "httpecho",
			downstreamNamespace: namespace("downstream-ns").Object(),
			downstreamEndpoints: endpoints("httpecho").WithNamespace("downstream-ns").Object(),
			downstreamService:   service("httpecho").WithNamespace("downstream-ns").Object(),
		},
		"Error on wrong namespace locator": {
			endpointName: "httpecho",
			downstreamNamespace: namespace("downstream-ns").WithAnnotations(map[string]string{
				"kcp.io/namespace-locator": "invalid json content",
			}).Object(),
			downstreamEndpoints: endpoints("httpecho").WithNamespace("downstream-ns").Object(),
			downstreamService:   service("httpecho").WithNamespace("downstream-ns").Object(),
			expectedError:       "invalid character 'i' looking for beginning of value",
		},
		"Don't label the endpoints when namespace locator syncTarget cluster name doesn't match": {
			endpointName: "httpecho",
			downstreamNamespace: namespace("downstream-ns").WithLocator(t, shared.NamespaceLocator{
				SyncTarget: shared.SyncTargetLocator{
					ClusterName: "another Cluster",
					Name:        defaultSyncTargetLocator.Name,
					UID:         defaultSyncTargetLocator.UID,
				},
				ClusterName: logicalcluster.Name("root:org:ws"),
				Namespace:   "ns",
			}).Object(),
			downstreamEndpoints: endpoints("httpecho").WithNamespace("downstream-ns").Object(),
			downstreamService:   service("httpecho").WithNamespace("downstream-ns").Object(),
		},
		"Don't label the endpoints when namespace locator syncTarget name doesn't match": {
			endpointName: "httpecho",
			downstreamNamespace: namespace("downstream-ns").WithLocator(t, shared.NamespaceLocator{
				SyncTarget: shared.SyncTargetLocator{
					ClusterName: defaultSyncTargetLocator.ClusterName,
					Name:        "anotherName",
					UID:         defaultSyncTargetLocator.UID,
				},
				ClusterName: logicalcluster.Name("root:org:ws"),
				Namespace:   "ns",
			}).Object(),
			downstreamEndpoints: endpoints("httpecho").WithNamespace("downstream-ns").Object(),
			downstreamService:   service("httpecho").WithNamespace("downstream-ns").Object(),
		},
		"Don't label the endpoints when namespace locator syncTarget UID doesn't match": {
			endpointName: "httpecho",
			downstreamNamespace: namespace("downstream-ns").WithLocator(t, shared.NamespaceLocator{
				SyncTarget: shared.SyncTargetLocator{
					ClusterName: defaultSyncTargetLocator.ClusterName,
					Name:        defaultSyncTargetLocator.Name,
					UID:         types.UID("anotherUID"),
				},
				ClusterName: logicalcluster.Name("root:org:ws"),
				Namespace:   "ns",
			}).Object(),
			downstreamEndpoints: endpoints("httpecho").WithNamespace("downstream-ns").Object(),
			downstreamService:   service("httpecho").WithNamespace("downstream-ns").Object(),
		},
		"Don't label the endpoints when endpoints resource is not found": {
			endpointName: "httpecho",
			downstreamNamespace: namespace("downstream-ns").WithLocator(t, shared.NamespaceLocator{SyncTarget: defaultSyncTargetLocator,
				ClusterName: logicalcluster.Name("root:org:ws"),
				Namespace:   "ns",
			}).Object(),
			downstreamService: service("httpecho").WithNamespace("downstream-ns").Object(),
		},
		"Error when endpoints retrieval fails": {
			endpointName: "httpecho",
			downstreamNamespace: namespace("downstream-ns").WithLocator(t, shared.NamespaceLocator{SyncTarget: defaultSyncTargetLocator,
				ClusterName: logicalcluster.Name("root:org:ws"),
				Namespace:   "ns",
			}).Object(),
			endpointsGetError: errors.New("error"),
			downstreamService: service("httpecho").WithNamespace("downstream-ns").Object(),
			expectedError:     "error",
		},
		"Don't label the endpoints when endpoints is labelled for upsync already": {
			endpointName: "httpecho",
			downstreamNamespace: namespace("downstream-ns").WithLocator(t, shared.NamespaceLocator{SyncTarget: defaultSyncTargetLocator,
				ClusterName: logicalcluster.Name("root:org:ws"),
				Namespace:   "ns",
			}).Object(),
			downstreamEndpoints: endpoints("httpecho").WithNamespace("downstream-ns").WithLabels(map[string]string{
				"state.workload.kcp.io/" + workloadv1alpha1.ToSyncTargetKey(logicalcluster.Name(defaultSyncTargetLocator.ClusterName), defaultSyncTargetLocator.Name): "Upsync",
			}).Object(),
			downstreamService: service("httpecho").WithNamespace("downstream-ns").Object(),
		},
		"Don't label the endpoints when service resource is not found": {
			endpointName: "httpecho",
			downstreamNamespace: namespace("downstream-ns").WithLocator(t, shared.NamespaceLocator{SyncTarget: defaultSyncTargetLocator,
				ClusterName: logicalcluster.Name("root:org:ws"),
				Namespace:   "ns",
			}).Object(),
			downstreamEndpoints: endpoints("httpecho").WithNamespace("downstream-ns").Object(),
		},
		"Error when service retrieval fails": {
			endpointName: "httpecho",
			downstreamNamespace: namespace("downstream-ns").WithLocator(t, shared.NamespaceLocator{SyncTarget: defaultSyncTargetLocator,
				ClusterName: logicalcluster.Name("root:org:ws"),
				Namespace:   "ns",
			}).Object(),
			downstreamEndpoints: endpoints("httpecho").WithNamespace("downstream-ns").Object(),
			serviceGetError:     errors.New("error"),
			expectedError:       "error",
		},
		"Don't label the endpoints when service has an owner refs": {
			endpointName: "httpecho",
			downstreamNamespace: namespace("downstream-ns").WithLocator(t, shared.NamespaceLocator{SyncTarget: defaultSyncTargetLocator,
				ClusterName: logicalcluster.Name("root:org:ws"),
				Namespace:   "ns",
			}).Object(),
			downstreamEndpoints: endpoints("httpecho").WithNamespace("downstream-ns").Object(),
			downstreamService:   service("httpecho").WithNamespace("downstream-ns").WithOwnerRef().Object(),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			if tc.syncTargetLocator == nil {
				tc.syncTargetLocator = &defaultSyncTargetLocator
			}

			syncTargetKey := workloadv1alpha1.ToSyncTargetKey(logicalcluster.Name(tc.syncTargetLocator.ClusterName), tc.syncTargetLocator.Name)

			actualPatch := ""
			var actualPatchType types.PatchType
			controller := controller{
				queue: workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),

				syncTargetName:        tc.syncTargetLocator.Name,
				syncTargetClusterName: logicalcluster.Name(tc.syncTargetLocator.ClusterName),
				syncTargetUID:         tc.syncTargetLocator.UID,
				syncTargetKey:         syncTargetKey,

				getDownstreamResource: func(gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
					var obj runtime.Object
					switch gvr {
					case endpointsGVR:
						if tc.endpointsGetError != nil {
							return nil, tc.endpointsGetError
						}
						if tc.downstreamEndpoints == nil {
							return nil, apierrors.NewNotFound(gvr.GroupResource(), name)
						}
						obj = tc.downstreamEndpoints
					case servicesGVR:
						if tc.serviceGetError != nil {
							return nil, tc.serviceGetError
						}
						if tc.downstreamService == nil {
							return nil, apierrors.NewNotFound(gvr.GroupResource(), name)
						}
						obj = tc.downstreamService
					}
					var unstr unstructured.Unstructured
					err := scheme.Convert(obj, &unstr, nil)
					require.NoError(t, err)
					return &unstr, nil
				},
				getDownstreamNamespace: func(name string) (*unstructured.Unstructured, error) {
					if tc.namespaceGetError != nil {
						return nil, tc.namespaceGetError
					}
					if tc.downstreamNamespace == nil {
						return nil, apierrors.NewNotFound(namespacesGVR.GroupResource(), name)
					}
					var unstr unstructured.Unstructured
					err := scheme.Convert(tc.downstreamNamespace, &unstr, nil)
					require.NoError(t, err)
					return &unstr, nil
				},
				patchEndpoint: func(ctx context.Context, namespace, name string, pt types.PatchType, data []byte) error {
					actualPatch = string(data)
					actualPatchType = pt
					return nil
				},
			}

			namespaceName := ""
			if tc.downstreamNamespace != nil {
				namespaceName = tc.downstreamNamespace.Name
			}
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(&metav1.ObjectMeta{
				Name:      tc.endpointName,
				Namespace: namespaceName,
			})
			require.NoError(t, err)
			err = controller.process(ctx, key)

			if tc.expectedError != "" {
				require.EqualError(t, err, tc.expectedError)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, tc.expectedPatch, actualPatch)
			require.Equal(t, tc.expectedPatchType, actualPatchType)
		})
	}
}

type resourceBuilder[Type metav1.Object] struct {
	obj Type
}

func (r *resourceBuilder[Type]) Object() Type {
	return r.obj
}

func (r *resourceBuilder[Type]) WithNamespace(namespace string) *resourceBuilder[Type] {
	r.obj.SetNamespace(namespace)
	return r
}

func (r *resourceBuilder[Type]) WithOwnerRef() *resourceBuilder[Type] {
	r.obj.SetOwnerReferences([]metav1.OwnerReference{
		{
			Name: "name",
		},
	})
	return r
}

func (r *resourceBuilder[Type]) WithLocator(t *testing.T, locator shared.NamespaceLocator) *resourceBuilder[Type] {
	locatorJSON, err := json.Marshal(locator)
	require.NoError(t, err)
	r.WithAnnotations(map[string]string{
		shared.NamespaceLocatorAnnotation: string(locatorJSON),
	})
	return r
}

func (r *resourceBuilder[Type]) WithAnnotations(additionalAnnotations map[string]string) *resourceBuilder[Type] {
	annotations := r.obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	for k, v := range additionalAnnotations {
		annotations[k] = v
	}

	r.obj.SetAnnotations(annotations)
	return r
}

func (r *resourceBuilder[Type]) WithLabels(additionalLabels map[string]string) *resourceBuilder[Type] {
	labels := r.obj.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	for k, v := range additionalLabels {
		labels[k] = v
	}

	r.obj.SetLabels(labels)
	return r
}

func newResourceBuilder[Type metav1.Object](obj Type, name string) *resourceBuilder[Type] {
	obj.SetName(name)
	return &resourceBuilder[Type]{obj}
}

func namespace(name string) *resourceBuilder[*corev1.Namespace] {
	return newResourceBuilder(&corev1.Namespace{}, name)
}

func endpoints(name string) *resourceBuilder[*corev1.Endpoints] {
	return newResourceBuilder(&corev1.Endpoints{}, name)
}

func service(name string) *resourceBuilder[*corev1.Service] {
	return newResourceBuilder(&corev1.Service{}, name)
}
