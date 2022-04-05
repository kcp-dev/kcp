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
	"net/url"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilspointer "k8s.io/utils/pointer"
)

type DeploymentMutator struct {
	externalURL *url.URL
}

func (dm *DeploymentMutator) GVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "apps",
		Version:  "v1",
		Resource: "deployments",
	}
}

func NewDeploymentMutator(externalURL *url.URL) *DeploymentMutator {
	return &DeploymentMutator{
		externalURL: externalURL,
	}
}

// Mutate applies the mutator changes to the object.
func (dm *DeploymentMutator) Mutate(downstreamObj *unstructured.Unstructured) error {
	var deployment appsv1.Deployment
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(
		downstreamObj.UnstructuredContent(),
		&deployment)
	if err != nil {
		return err
	}

	// If the deployment has no serviceaccount defined or it is "default", that means that it is using the default one
	// and we need will need to override it with our own, "kcp-default"
	//
	// If the deployment has a serviceaccount defined, that means we need to synchronize the created service account
	// down to the workloadcluster, so we don't modify it, and expect the scheduler to do the job.
	//
	// Alias for the template.spec to improve readability.
	templateSpec := &deployment.Spec.Template.Spec

	if templateSpec.ServiceAccountName == "" || templateSpec.ServiceAccountName == "default" {
		templateSpec.ServiceAccountName = "kcp-default"
	}

	// Setting AutomountServiceAccountToken to false allow us to control the ServiceAccount
	// VolumeMount and Volume definitions.
	templateSpec.AutomountServiceAccountToken = utilspointer.BoolPtr(false)

	overrideEnvs := []corev1.EnvVar{
		{Name: "KUBERNETES_SERVICE_PORT", Value: dm.externalURL.Port()},
		{Name: "KUBERNETES_SERVICE_PORT_HTTPS", Value: dm.externalURL.Port()},
		{Name: "KUBERNETES_SERVICE_HOST", Value: dm.externalURL.Hostname()},
	}

	// This is the VolumeMount that we will append to all the containers of the deployment
	serviceAccountMount := corev1.VolumeMount{
		Name:      "kcp-api-access",
		MountPath: "/var/run/secrets/kubernetes.io/serviceaccount",
		ReadOnly:  true,
	}

	// This is the Volume that we will add to the Deployment in order to control
	// the name of the ca.crt references (kcp-root-ca.crt vs kube-root-ca.crt)
	// and the serviceaccount reference.
	serviceAccountVolume := corev1.Volume{
		Name: "kcp-api-access",
		VolumeSource: corev1.VolumeSource{
			Projected: &corev1.ProjectedVolumeSource{
				DefaultMode: utilspointer.Int32Ptr(420),
				Sources: []corev1.VolumeProjection{
					{
						// TODO(jmprusi): Investigate if instead of using a secret directly we should use the serviceaccount
						//                in order to get a bound token. We will need to investigate this
						//                as the pcluster keeps rewriting the serviceaccount and its tokens values rendering
						//                them non-valid for KCP. (Also it removes the ClusterName included in the JWT token)
						Secret: &corev1.SecretProjection{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "kcp-default-token",
							},
							Items: []corev1.KeyToPath{
								{
									Key:  "token",
									Path: "token",
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
					{
						DownwardAPI: &corev1.DownwardAPIProjection{
							Items: []corev1.DownwardAPIVolumeFile{
								{
									Path: "namespace",
									FieldRef: &corev1.ObjectFieldSelector{
										APIVersion: "v1",
										FieldPath:  "metadata.namespace",
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// Override Envs and add the VolumeMount to all the containers
	for i := range deployment.Spec.Template.Spec.Containers {
		for _, overrideEnv := range overrideEnvs {
			templateSpec.Containers[i].Env = updateEnv(templateSpec.Containers[i].Env, overrideEnv)
		}
		templateSpec.Containers[i].VolumeMounts = updateVolumeMount(templateSpec.Containers[i].VolumeMounts, serviceAccountMount)
	}

	// Override Envs and add the VolumeMount to all the Init containers
	for i := range templateSpec.InitContainers {
		for _, overrideEnv := range overrideEnvs {
			templateSpec.InitContainers[i].Env = updateEnv(templateSpec.InitContainers[i].Env, overrideEnv)
		}
		templateSpec.InitContainers[i].VolumeMounts = updateVolumeMount(templateSpec.InitContainers[i].VolumeMounts, serviceAccountMount)
	}

	// Override Envs and add the VolumeMount to all the Ephemeral containers
	for i := range templateSpec.EphemeralContainers {
		for _, overrideEnv := range overrideEnvs {
			templateSpec.EphemeralContainers[i].Env = updateEnv(templateSpec.EphemeralContainers[i].Env, overrideEnv)
		}
		templateSpec.EphemeralContainers[i].VolumeMounts = updateVolumeMount(templateSpec.EphemeralContainers[i].VolumeMounts, serviceAccountMount)
	}

	// Add the ServiceAccount volume with our overrides.
	found := false
	for i := range templateSpec.Volumes {
		if templateSpec.Volumes[i].Name == "kcp-api-access" {
			templateSpec.Volumes[i] = serviceAccountVolume
			found = true
		}
	}
	if !found {
		templateSpec.Volumes = append(templateSpec.Volumes, serviceAccountVolume)
	}

	unstructured, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&deployment)
	if err != nil {
		return err
	}

	// Set the changes back into the obj.
	downstreamObj.SetUnstructuredContent(unstructured)

	return nil
}

// findEnv finds an env in a list of envs
func findEnv(envs []corev1.EnvVar, name string) (bool, int) {
	for i := range envs {
		if envs[i].Name == name {
			return true, i
		}
	}
	return false, 0
}

// updateEnv updates an env from a list of envs
func updateEnv(envs []corev1.EnvVar, overrideEnv corev1.EnvVar) []corev1.EnvVar {
	found, i := findEnv(envs, overrideEnv.Name)
	if found {
		envs[i].Value = overrideEnv.Value
	} else {
		envs = append(envs, overrideEnv)
	}

	return envs
}

// findVolumeMount finds a volume mount in a list of volume mounts
func findVolumeMount(volumeMounts []corev1.VolumeMount, name string) (bool, int) {
	for i := range volumeMounts {
		if volumeMounts[i].Name == name {
			return true, i
		}
	}
	return false, 0
}

// updateVolumeMount updates a volume mount from a list of volume mounts
func updateVolumeMount(volumeMounts []corev1.VolumeMount, overrideVolumeMount corev1.VolumeMount) []corev1.VolumeMount {
	found, i := findVolumeMount(volumeMounts, overrideVolumeMount.Name)
	if found {
		volumeMounts[i] = overrideVolumeMount
	} else {
		volumeMounts = append(volumeMounts, overrideVolumeMount)
	}

	return volumeMounts
}
