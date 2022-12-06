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
	"fmt"
	"net/url"
	"sort"

	"github.com/kcp-dev/logicalcluster/v3"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	listerscorev1 "k8s.io/client-go/listers/core/v1"
	utilspointer "k8s.io/utils/pointer"

	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

type ListSecretFunc func(clusterName logicalcluster.Path, namespace string) ([]runtime.Object, error)

type DeploymentMutator struct {
	upstreamURL                  *url.URL
	listSecrets                  ListSecretFunc
	serviceLister                listerscorev1.ServiceLister
	syncTargetLogicalClusterName logicalcluster.Path
	syncTargetUID                types.UID
	syncTargetName               string
	dnsNamespace                 string
}

func (dm *DeploymentMutator) GVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "apps",
		Version:  "v1",
		Resource: "deployments",
	}
}

func NewDeploymentMutator(upstreamURL *url.URL, secretLister ListSecretFunc, serviceLister listerscorev1.ServiceLister,
	syncTargetLogicalClusterName logicalcluster.Path,
	syncTargetUID types.UID, syncTargetName, dnsNamespace string) *DeploymentMutator {

	return &DeploymentMutator{
		upstreamURL:                  upstreamURL,
		listSecrets:                  secretLister,
		serviceLister:                serviceLister,
		syncTargetLogicalClusterName: syncTargetLogicalClusterName,
		syncTargetUID:                syncTargetUID,
		syncTargetName:               syncTargetName,
		dnsNamespace:                 dnsNamespace,
	}
}

// Mutate applies the mutator changes to the object.
func (dm *DeploymentMutator) Mutate(obj *unstructured.Unstructured) error {
	var deployment appsv1.Deployment
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(
		obj.UnstructuredContent(),
		&deployment)
	if err != nil {
		return err
	}
	upstreamLogicalName := logicalcluster.From(obj)

	templateSpec := &deployment.Spec.Template.Spec

	desiredServiceAccountName := "default"
	if templateSpec.ServiceAccountName != "" && templateSpec.ServiceAccountName != "default" {
		desiredServiceAccountName = templateSpec.ServiceAccountName
	}

	rawSecretList, err := dm.listSecrets(upstreamLogicalName, deployment.Namespace)
	if err != nil {
		return fmt.Errorf("error listing secrets for workspace %s: %w", upstreamLogicalName.String(), err)
	}

	var secretList []*unstructured.Unstructured
	for i := range rawSecretList {
		secretList = append(secretList, rawSecretList[i].(*unstructured.Unstructured))
	}

	// In order to avoid triggering a deployment update on resyncs, we need to make sure that the list
	// of secrets is sorted by creationTimsestamp. So if the user creates a new token for a given serviceaccount
	// the first one will be picked always.
	sort.Slice(secretList, func(i, j int) bool {
		iCreationTimestamp := secretList[i].GetCreationTimestamp()
		jCreationTimestamp := secretList[j].GetCreationTimestamp()
		return iCreationTimestamp.Before(&jCreationTimestamp)
	})

	desiredSecretName := ""
	for _, secret := range secretList {
		// Find the SA token that matches the service account name.
		if val, ok := secret.GetAnnotations()[corev1.ServiceAccountNameKey]; ok && val == desiredServiceAccountName {
			if desiredServiceAccountName == "default" {
				desiredSecretName = "kcp-" + secret.GetName()
				break
			}
			desiredSecretName = secret.GetName()
			break
		}
	}

	if desiredSecretName == "" {
		return fmt.Errorf("couldn't find a token upstream for the serviceaccount %s/%s in workspace %s", desiredServiceAccountName, deployment.Namespace, upstreamLogicalName.String())
	}

	// Setting AutomountServiceAccountToken to false allow us to control the ServiceAccount
	// VolumeMount and Volume definitions.
	templateSpec.AutomountServiceAccountToken = utilspointer.BoolPtr(false)
	// Set to empty the serviceAccountName on podTemplate as we are not syncing the serviceAccount down to the workload cluster.
	templateSpec.ServiceAccountName = ""

	kcpExternalHost := dm.upstreamURL.Hostname()
	kcpExternalPort := dm.upstreamURL.Port()

	overrideEnvs := []corev1.EnvVar{
		{Name: "KUBERNETES_SERVICE_PORT", Value: kcpExternalPort},
		{Name: "KUBERNETES_SERVICE_PORT_HTTPS", Value: kcpExternalPort},
		{Name: "KUBERNETES_SERVICE_HOST", Value: kcpExternalHost},
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
						Secret: &corev1.SecretProjection{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: desiredSecretName,
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

	// Override Envs, resolve downwardAPI FieldRef and add the VolumeMount to all the containers
	for i := range deployment.Spec.Template.Spec.Containers {
		for _, overrideEnv := range overrideEnvs {
			templateSpec.Containers[i].Env = updateEnv(templateSpec.Containers[i].Env, overrideEnv)
		}
		templateSpec.Containers[i].Env = resolveDownwardAPIFieldRefEnv(templateSpec.Containers[i].Env, deployment)
		templateSpec.Containers[i].VolumeMounts = updateVolumeMount(templateSpec.Containers[i].VolumeMounts, serviceAccountMount)
	}

	// Override Envs, resolve downwardAPI FieldRef and add the VolumeMount to all the Init containers
	for i := range templateSpec.InitContainers {
		for _, overrideEnv := range overrideEnvs {
			templateSpec.InitContainers[i].Env = updateEnv(templateSpec.InitContainers[i].Env, overrideEnv)
		}
		templateSpec.InitContainers[i].Env = resolveDownwardAPIFieldRefEnv(templateSpec.InitContainers[i].Env, deployment)
		templateSpec.InitContainers[i].VolumeMounts = updateVolumeMount(templateSpec.InitContainers[i].VolumeMounts, serviceAccountMount)
	}

	// Override Envs, resolve downwardAPI FieldRef and add the VolumeMount to all the Ephemeral containers
	for i := range templateSpec.EphemeralContainers {
		for _, overrideEnv := range overrideEnvs {
			templateSpec.EphemeralContainers[i].Env = updateEnv(templateSpec.EphemeralContainers[i].Env, overrideEnv)
		}
		templateSpec.EphemeralContainers[i].Env = resolveDownwardAPIFieldRefEnv(templateSpec.EphemeralContainers[i].Env, deployment)
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

	// Overrides DNS to point to the workspace DNS
	dnsIP, err := dm.getDNSIPForWorkspace(upstreamLogicalName)
	if err != nil {
		// the DNS nameserver is not ready yet or other transient failure
		return err // retry
	}

	deployment.Spec.Template.Spec.DNSPolicy = corev1.DNSNone
	deployment.Spec.Template.Spec.DNSConfig = &corev1.PodDNSConfig{
		Nameservers: []string{dnsIP},
		Searches: []string{ // TODO(LV): from /etc/resolv.conf
			obj.GetNamespace() + ".svc.cluster.local",
			"svc.cluster.local",
			"cluster.local",
		},
		Options: []corev1.PodDNSConfigOption{
			{
				Name:  "ndots",
				Value: utilspointer.String("5"),
			},
		},
	}

	unstructured, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&deployment)
	if err != nil {
		return err
	}

	// Set the changes back into the obj.
	obj.SetUnstructuredContent(unstructured)

	return nil
}

func (dm *DeploymentMutator) getDNSIPForWorkspace(workspace logicalcluster.Path) (string, error) {
	// Retrieve the DNS IP associated to the workspace
	dnsServiceName := shared.GetDNSID(workspace, dm.syncTargetUID, dm.syncTargetName)

	svc, err := dm.serviceLister.Services(dm.dnsNamespace).Get(dnsServiceName)
	if err != nil {
		return "", fmt.Errorf("failed to get DNS service: %w", err)
	}

	ip := svc.Spec.ClusterIP
	if ip == "" {
		// not available (yet)
		return "", fmt.Errorf("DNS service IP address not found")
	}

	return ip, nil
}

// resolveDownwardAPIFieldRefEnv replaces the downwardAPI FieldRef EnvVars with the value from the deployment, right now it only replaces the metadata.namespace
func resolveDownwardAPIFieldRefEnv(envs []corev1.EnvVar, deployment appsv1.Deployment) []corev1.EnvVar {
	var result []corev1.EnvVar
	for _, env := range envs {
		if env.ValueFrom != nil && env.ValueFrom.FieldRef != nil && env.ValueFrom.FieldRef.FieldPath == "metadata.namespace" {
			result = append(result, corev1.EnvVar{
				Name:  env.Name,
				Value: deployment.Namespace,
			})
		} else {
			result = append(result, env)
		}
	}
	return result
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
