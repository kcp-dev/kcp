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

package mutators

import (
	"encoding/json"
	"fmt"
	"net/url"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	listerscorev1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	utilspointer "k8s.io/utils/pointer"

	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

var kcpApiAccessVolume = corev1.Volume{
	Name: "kcp-api-access",
	VolumeSource: corev1.VolumeSource{
		Projected: &corev1.ProjectedVolumeSource{
			DefaultMode: utilspointer.Int32Ptr(420),
			Sources: []corev1.VolumeProjection{
				{
					Secret: &corev1.SecretProjection{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: "kcp-default-token-1234",
						},
						Items: []corev1.KeyToPath{
							{
								Key:  "token",
								Path: "token",
							},
							{
								Key:  "namespace",
								Path: "namespace",
							},
						},
					},
				},
				{
					ConfigMap: &corev1.ConfigMapProjection{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: "kcp-root-ca.crt",
						},
						Items: []corev1.KeyToPath{
							{
								Key:  "ca.crt",
								Path: "ca.crt",
							},
						},
					},
				},
			},
		},
	},
}

var kcpApiAccessVolumeMount = corev1.VolumeMount{
	Name:      "kcp-api-access",
	MountPath: "/var/run/secrets/kubernetes.io/serviceaccount",
	ReadOnly:  true,
}

func TestDeploymentMutate(t *testing.T) {
	for _, c := range []struct {
		desc                                   string
		upstreamSecrets                        []*corev1.Secret
		originalDeployment, expectedDeployment *appsv1.Deployment
		config                                 *rest.Config
	}{{
		desc: "Deployment without Envs or volumes is mutated.",
		upstreamSecrets: []*corev1.Secret{
			{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Secret",
					APIVersion: "v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "default-token-1234",
					Namespace: "namespace",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey:         "root:default:testing",
						"kubernetes.io/service-account.name": "default",
					},
				},
				Data: map[string][]byte{
					"token":     []byte("token"),
					"namespace": []byte("namespace"),
				},
			},
		},
		originalDeployment: &appsv1.Deployment{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Deployment",
				APIVersion: "apps/v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-deployment",
				Namespace: "namespace",
				Annotations: map[string]string{
					logicalcluster.AnnotationKey: "root:default:testing",
				},
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: new(int32),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image",
							},
						},
					},
				},
			},
		},
		expectedDeployment: &appsv1.Deployment{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Deployment",
				APIVersion: "apps/v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-deployment",
				Namespace: "namespace",
				Annotations: map[string]string{
					logicalcluster.AnnotationKey: "root:default:testing",
				},
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: new(int32),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						AutomountServiceAccountToken: utilspointer.BoolPtr(false),
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image",
								Env: []corev1.EnvVar{
									{
										Name:  "KUBERNETES_SERVICE_PORT",
										Value: "12345",
									},
									{
										Name:  "KUBERNETES_SERVICE_PORT_HTTPS",
										Value: "12345",
									},
									{
										Name:  "KUBERNETES_SERVICE_HOST",
										Value: "4.5.6.7",
									},
								},
								VolumeMounts: []corev1.VolumeMount{
									kcpApiAccessVolumeMount,
								},
							},
						},
						DNSPolicy: corev1.DNSNone,
						DNSConfig: &corev1.PodDNSConfig{
							Nameservers: []string{"8.8.8.8"},
							Searches:    []string{"namespace.svc.cluster.local", "svc.cluster.local", "cluster.local"},
							Options:     []corev1.PodDNSConfigOption{{Name: "ndots", Value: utilspointer.String("5")}},
						},
						Volumes: []corev1.Volume{
							kcpApiAccessVolume,
						},
					},
				},
			},
		},
		config: &rest.Config{
			Host: "https://4.5.6.7:12345",
		},
	}, {
		desc: "Deployment with one env var gets mutated but the already existing env var remains the same",
		upstreamSecrets: []*corev1.Secret{
			{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Secret",
					APIVersion: "v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "default-token-1234",
					Namespace: "namespace",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey:         "root:default:testing",
						"kubernetes.io/service-account.name": "default",
					},
				},
				Data: map[string][]byte{
					"token":     []byte("token"),
					"namespace": []byte("namespace"),
				},
			},
		},
		originalDeployment: &appsv1.Deployment{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Deployment",
				APIVersion: "apps/v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-deployment",
				Namespace: "namespace",
				Annotations: map[string]string{
					logicalcluster.AnnotationKey: "root:default:testing",
				},
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: new(int32),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image",
								Env: []corev1.EnvVar{
									{
										Name:  "TEST_ENV_VAR",
										Value: "test-value",
									},
								},
							},
						},
					},
				},
			},
		},
		expectedDeployment: &appsv1.Deployment{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Deployment",
				APIVersion: "apps/v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-deployment",
				Namespace: "namespace",
				Annotations: map[string]string{
					logicalcluster.AnnotationKey: "root:default:testing",
				},
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: new(int32),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						AutomountServiceAccountToken: utilspointer.BoolPtr(false),
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image",
								Env: []corev1.EnvVar{
									{
										Name:  "TEST_ENV_VAR",
										Value: "test-value",
									},
									{
										Name:  "KUBERNETES_SERVICE_PORT",
										Value: "12345",
									},
									{
										Name:  "KUBERNETES_SERVICE_PORT_HTTPS",
										Value: "12345",
									},
									{
										Name:  "KUBERNETES_SERVICE_HOST",
										Value: "4.5.6.7",
									},
								},
								VolumeMounts: []corev1.VolumeMount{
									kcpApiAccessVolumeMount,
								},
							},
						},
						DNSPolicy: corev1.DNSNone,
						DNSConfig: &corev1.PodDNSConfig{
							Nameservers: []string{"8.8.8.8"},
							Searches:    []string{"namespace.svc.cluster.local", "svc.cluster.local", "cluster.local"},
							Options:     []corev1.PodDNSConfigOption{{Name: "ndots", Value: utilspointer.String("5")}},
						},
						Volumes: []corev1.Volume{
							kcpApiAccessVolume,
						},
					},
				},
			},
		},
		config: &rest.Config{
			Host: "https://4.5.6.7:12345",
		},
	},
		{desc: "Deployment with an env var named KUBERNETES_SERVICE_PORT gets mutated and it is overridden and not duplicated",
			upstreamSecrets: []*corev1.Secret{
				{
					TypeMeta: metav1.TypeMeta{
						Kind:       "Secret",
						APIVersion: "v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "default-token-1234",
						Namespace: "namespace",
						Annotations: map[string]string{
							logicalcluster.AnnotationKey:         "root:default:testing",
							"kubernetes.io/service-account.name": "default",
						},
					},
					Data: map[string][]byte{
						"token":     []byte("token"),
						"namespace": []byte("namespace"),
					},
				},
			},
			originalDeployment: &appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Deployment",
					APIVersion: "apps/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: "namespace",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "root:default:testing",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: new(int32),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test-container",
									Image: "test-image",
									Env: []corev1.EnvVar{
										{
											Name:  "KUBERNETES_SERVICE_PORT",
											Value: "99999",
										},
									},
								},
							},
						},
					},
				},
			},
			expectedDeployment: &appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Deployment",
					APIVersion: "apps/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: "namespace",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "root:default:testing",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: new(int32),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							AutomountServiceAccountToken: utilspointer.BoolPtr(false),
							Containers: []corev1.Container{
								{
									Name:  "test-container",
									Image: "test-image",
									Env: []corev1.EnvVar{
										{
											Name:  "KUBERNETES_SERVICE_PORT",
											Value: "12345",
										},
										{
											Name:  "KUBERNETES_SERVICE_PORT_HTTPS",
											Value: "12345",
										},
										{
											Name:  "KUBERNETES_SERVICE_HOST",
											Value: "4.5.6.7",
										},
									},
									VolumeMounts: []corev1.VolumeMount{
										kcpApiAccessVolumeMount,
									},
								},
							},
							DNSPolicy: corev1.DNSNone,
							DNSConfig: &corev1.PodDNSConfig{
								Nameservers: []string{"8.8.8.8"},
								Searches:    []string{"namespace.svc.cluster.local", "svc.cluster.local", "cluster.local"},
								Options:     []corev1.PodDNSConfigOption{{Name: "ndots", Value: utilspointer.String("5")}},
							},
							Volumes: []corev1.Volume{
								kcpApiAccessVolume,
							},
						},
					},
				},
			},
			config: &rest.Config{
				Host: "https://4.5.6.7:12345",
			}},
		{desc: "Deployment with an existing VolumeMount named kcp-api-access gets mutated and it is overridden and not duplicated",
			upstreamSecrets: []*corev1.Secret{
				{
					TypeMeta: metav1.TypeMeta{
						Kind:       "Secret",
						APIVersion: "v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "default-token-1234",
						Namespace: "namespace",
						Annotations: map[string]string{
							logicalcluster.AnnotationKey:         "root:default:testing",
							"kubernetes.io/service-account.name": "default",
						},
					},
					Data: map[string][]byte{
						"token":     []byte("token"),
						"namespace": []byte("namespace"),
					},
				},
			},
			originalDeployment: &appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Deployment",
					APIVersion: "apps/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: "namespace",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "root:default:testing",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: new(int32),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test-container",
									Image: "test-image",
									Env: []corev1.EnvVar{
										{
											Name:  "KUBERNETES_SERVICE_PORT",
											Value: "99999",
										},
									},
									VolumeMounts: []corev1.VolumeMount{
										{
											Name:      "kcp-api-access",
											MountPath: "totally-incorrect-path",
											ReadOnly:  false,
										},
									},
								},
							},
						},
					},
				},
			},
			expectedDeployment: &appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Deployment",
					APIVersion: "apps/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: "namespace",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "root:default:testing",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: new(int32),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							AutomountServiceAccountToken: utilspointer.BoolPtr(false),
							Containers: []corev1.Container{
								{
									Name:  "test-container",
									Image: "test-image",
									Env: []corev1.EnvVar{
										{
											Name:  "KUBERNETES_SERVICE_PORT",
											Value: "12345",
										},
										{
											Name:  "KUBERNETES_SERVICE_PORT_HTTPS",
											Value: "12345",
										},
										{
											Name:  "KUBERNETES_SERVICE_HOST",
											Value: "4.5.6.7",
										},
									},
									VolumeMounts: []corev1.VolumeMount{
										kcpApiAccessVolumeMount,
									},
								},
							},
							DNSPolicy: corev1.DNSNone,
							DNSConfig: &corev1.PodDNSConfig{
								Nameservers: []string{"8.8.8.8"},
								Searches:    []string{"namespace.svc.cluster.local", "svc.cluster.local", "cluster.local"},
								Options:     []corev1.PodDNSConfigOption{{Name: "ndots", Value: utilspointer.String("5")}},
							},
							Volumes: []corev1.Volume{
								kcpApiAccessVolume,
							},
						},
					},
				},
			},
			config: &rest.Config{
				Host: "https://4.5.6.7:12345",
			}},
		{desc: "Deployment with an existing Volume named kcp-api-access gets mutated and it is overridden and not duplicated",
			upstreamSecrets: []*corev1.Secret{
				{
					TypeMeta: metav1.TypeMeta{
						Kind:       "Secret",
						APIVersion: "v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "default-token-1234",
						Namespace: "namespace",
						Annotations: map[string]string{
							logicalcluster.AnnotationKey:         "root:default:testing",
							"kubernetes.io/service-account.name": "default",
						},
					},
					Data: map[string][]byte{
						"token":     []byte("token"),
						"namespace": []byte("namespace"),
					},
				},
			},
			originalDeployment: &appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Deployment",
					APIVersion: "apps/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: "namespace",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "root:default:testing",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: new(int32),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test-container",
									Image: "test-image",
									Env: []corev1.EnvVar{
										{
											Name:  "KUBERNETES_SERVICE_PORT",
											Value: "99999",
										},
									},
									VolumeMounts: []corev1.VolumeMount{
										{
											Name:      "kcp-api-access",
											MountPath: "totally-not-the-path",
											ReadOnly:  false,
										},
									},
								},
							},
							Volumes: []corev1.Volume{
								{
									Name: "kcp-api-access",
									VolumeSource: corev1.VolumeSource{
										Secret: &corev1.SecretVolumeSource{
											SecretName: "this-is-not-the-secret-you-are-looking-for",
										},
									},
								},
							},
						},
					},
				},
			},
			expectedDeployment: &appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Deployment",
					APIVersion: "apps/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: "namespace",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "root:default:testing",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: new(int32),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							AutomountServiceAccountToken: utilspointer.BoolPtr(false),
							Containers: []corev1.Container{
								{
									Name:  "test-container",
									Image: "test-image",
									Env: []corev1.EnvVar{
										{
											Name:  "KUBERNETES_SERVICE_PORT",
											Value: "12345",
										},
										{
											Name:  "KUBERNETES_SERVICE_PORT_HTTPS",
											Value: "12345",
										},
										{
											Name:  "KUBERNETES_SERVICE_HOST",
											Value: "4.5.6.7",
										},
									},
									VolumeMounts: []corev1.VolumeMount{
										kcpApiAccessVolumeMount,
									},
								},
							},
							DNSPolicy: corev1.DNSNone,
							DNSConfig: &corev1.PodDNSConfig{
								Nameservers: []string{"8.8.8.8"},
								Searches:    []string{"namespace.svc.cluster.local", "svc.cluster.local", "cluster.local"},
								Options:     []corev1.PodDNSConfigOption{{Name: "ndots", Value: utilspointer.String("5")}},
							},
							Volumes: []corev1.Volume{
								kcpApiAccessVolume,
							},
						},
					},
				},
			},
			config: &rest.Config{
				Host: "https://4.5.6.7:12345",
			}},
		{desc: "Deployment with an EnvVar value coming from the DownwardAPI,only the metadata.namespace should be made static",
			upstreamSecrets: []*corev1.Secret{
				{
					TypeMeta: metav1.TypeMeta{
						Kind:       "Secret",
						APIVersion: "v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "default-token-1234",
						Namespace: "namespace",
						Annotations: map[string]string{
							logicalcluster.AnnotationKey:         "root:default:testing",
							"kubernetes.io/service-account.name": "default",
						},
					},
					Data: map[string][]byte{
						"token":     []byte("token"),
						"namespace": []byte("namespace"),
					},
				},
			},
			originalDeployment: &appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Deployment",
					APIVersion: "apps/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: "namespace",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "root:default:testing",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: new(int32),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test-container",
									Image: "test-image",
									Env: []corev1.EnvVar{
										{
											Name: "DOWNWARDAPI_ENV_NAMESPACE",
											ValueFrom: &corev1.EnvVarSource{
												FieldRef: &corev1.ObjectFieldSelector{
													FieldPath: "metadata.namespace",
												},
											},
										},
										{
											Name: "DOWNWARDAPI_ENV_NAME",
											ValueFrom: &corev1.EnvVarSource{
												FieldRef: &corev1.ObjectFieldSelector{
													FieldPath: "metadata.name",
												},
											},
										},
										{
											Name:  "MYENV",
											Value: "myenv",
										},
									},
								},
							},
						},
					},
				},
			},
			expectedDeployment: &appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Deployment",
					APIVersion: "apps/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: "namespace",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "root:default:testing",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: new(int32),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							AutomountServiceAccountToken: utilspointer.BoolPtr(false),
							Containers: []corev1.Container{
								{
									Name:  "test-container",
									Image: "test-image",
									Env: []corev1.EnvVar{
										{
											Name:  "DOWNWARDAPI_ENV_NAMESPACE",
											Value: "namespace",
										},
										{
											Name: "DOWNWARDAPI_ENV_NAME",
											ValueFrom: &corev1.EnvVarSource{
												FieldRef: &corev1.ObjectFieldSelector{
													FieldPath: "metadata.name",
												},
											},
										},
										{
											Name:  "MYENV",
											Value: "myenv",
										},
										{
											Name:  "KUBERNETES_SERVICE_PORT",
											Value: "12345",
										},
										{
											Name:  "KUBERNETES_SERVICE_PORT_HTTPS",
											Value: "12345",
										},
										{
											Name:  "KUBERNETES_SERVICE_HOST",
											Value: "4.5.6.7",
										},
									},
									VolumeMounts: []corev1.VolumeMount{
										kcpApiAccessVolumeMount,
									},
								},
							},
							DNSPolicy: corev1.DNSNone,
							DNSConfig: &corev1.PodDNSConfig{
								Nameservers: []string{"8.8.8.8"},
								Searches:    []string{"namespace.svc.cluster.local", "svc.cluster.local", "cluster.local"},
								Options:     []corev1.PodDNSConfigOption{{Name: "ndots", Value: utilspointer.String("5")}},
							},
							Volumes: []corev1.Volume{
								kcpApiAccessVolume,
							},
						},
					},
				},
			},
			config: &rest.Config{
				Host: "https://4.5.6.7:12345",
			}},
	} {
		{
			t.Run(c.desc, func(t *testing.T) {
				upstreamURL, err := url.Parse(c.config.Host)
				require.NoError(t, err)

				secretLister := func(upstreamLogicalCluster logicalcluster.Name, namespace string) ([]runtime.Object, error) {
					unstructuredObjects := make([]runtime.Object, 0, len(c.upstreamSecrets))
					for _, obj := range c.upstreamSecrets {
						unstObj, err := toUnstructured(obj)
						require.NoError(t, err)
						unstructuredObjects = append(unstructuredObjects, unstObj)
					}
					return unstructuredObjects, nil
				}

				clusterName := logicalcluster.Name("root:default:testing")

				serviceIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})

				dnsServiceName := shared.GetDNSID(clusterName, "syncTargetUID", "syncTargetName")
				err = serviceIndexer.Add(service(dnsServiceName, "dnsNamespace"))
				require.NoError(t, err, "Service Add() = %v", err)
				svcLister := listerscorev1.NewServiceLister(serviceIndexer)

				dm := NewDeploymentMutator(upstreamURL, secretLister, svcLister, clusterName, "syncTargetUID", "syncTargetName", "dnsNamespace")

				unstrOriginalDeployment, err := toUnstructured(c.originalDeployment)
				require.NoError(t, err, "toUnstructured() = %v", err)

				err = dm.Mutate(unstrOriginalDeployment)
				require.NoError(t, err, "Mutate() = %v", err)

				mutatedOriginalDeployment, err := toDeployment(unstrOriginalDeployment)
				require.NoError(t, err, "toDeployment() = %v", err)

				if !apiequality.Semantic.DeepEqual(mutatedOriginalDeployment, c.expectedDeployment) {
					t.Errorf("expected deployments are not equal, got:\n %#v \n wanted:\n %#v \n", c.expectedDeployment, mutatedOriginalDeployment)
				}
			})
		}
	}
}

func toUnstructured(obj runtime.Object) (*unstructured.Unstructured, error) {
	bs, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("Marshal() = %w", err)
	}
	u := &unstructured.Unstructured{}
	if err := json.Unmarshal(bs, u); err != nil {
		return nil, fmt.Errorf("Unmarshal() = %w", err)
	}
	return u, nil
}

func toDeployment(obj *unstructured.Unstructured) (*appsv1.Deployment, error) {
	bs, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("Marshal() = %w", err)
	}
	d := &appsv1.Deployment{}
	if err := json.Unmarshal(bs, d); err != nil {
		return nil, fmt.Errorf("Unmarshal() = %w", err)
	}
	return d, nil
}

func service(name, namespace string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "8.8.8.8",
		},
	}
}
