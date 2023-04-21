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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	listerscorev1 "k8s.io/client-go/listers/core/v1"
	utilspointer "k8s.io/utils/pointer"

	ddsif "github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	workloadv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
)

type ListSecretFunc func(clusterName logicalcluster.Name, namespace string) ([]runtime.Object, error)
type GetWorkspaceURLFunc func(obj *unstructured.Unstructured) (*url.URL, error)
type GetClusterDDSIFFunc func(clusterName logicalcluster.Name) (*ddsif.DiscoveringDynamicSharedInformerFactory, error)

type PodSpecableMutator struct {
	getWorkspaceURL       GetWorkspaceURLFunc
	listSecrets           ListSecretFunc
	serviceLister         listerscorev1.ServiceLister
	syncTargetClusterName logicalcluster.Name
	syncTargetUID         types.UID
	syncTargetName        string
	dnsNamespace          string
	upsyncPods            bool
}

func (dm *PodSpecableMutator) GVRs() []schema.GroupVersionResource {
	return []schema.GroupVersionResource{
		{
			Group:    "apps",
			Version:  "v1",
			Resource: "deployments",
		},
		{
			Group:    "apps",
			Version:  "v1",
			Resource: "statefulsets",
		},
		{
			Group:    "apps",
			Version:  "v1",
			Resource: "replicasets",
		},
	}
}

func NewPodspecableMutator(ddsifForUpstreamSyncer GetClusterDDSIFFunc, serviceLister listerscorev1.ServiceLister,
	syncTargetClusterName logicalcluster.Name, syncTargetName string, syncTargetUID types.UID,
	dnsNamespace string, upsyncPods bool) *PodSpecableMutator {
	secretsGVR := corev1.SchemeGroupVersion.WithResource("secrets")
	return &PodSpecableMutator{
		getWorkspaceURL: func(obj *unstructured.Unstructured) (*url.URL, error) {
			workspaceURL, ok := obj.GetAnnotations()[workloadv1alpha1.InternalWorkspaceURLAnnotationKey]
			if !ok {
				return nil, fmt.Errorf("annotation %q not found on %s|%s/%s", workloadv1alpha1.InternalWorkspaceURLAnnotationKey, logicalcluster.From(obj), obj.GetNamespace(), obj.GetName())
			}
			return url.Parse(workspaceURL)
		},
		listSecrets: func(clusterName logicalcluster.Name, namespace string) ([]runtime.Object, error) {
			ddsif, err := ddsifForUpstreamSyncer(clusterName)
			if err != nil {
				return nil, err
			}
			informers, notSynced := ddsif.Informers()
			informer, ok := informers[secretsGVR]
			if !ok {
				if shared.ContainsGVR(notSynced, secretsGVR) {
					return nil, fmt.Errorf("informer for gvr %v not synced in the upstream syncer informer factory", secretsGVR)
				}
				return nil, fmt.Errorf("gvr %v should be known in the upstream syncer informer factory", secretsGVR)
			}
			return informer.Lister().ByCluster(clusterName).ByNamespace(namespace).List(labels.Everything())
		},
		serviceLister:         serviceLister,
		syncTargetClusterName: syncTargetClusterName,
		syncTargetUID:         syncTargetUID,
		syncTargetName:        syncTargetName,
		dnsNamespace:          dnsNamespace,
		upsyncPods:            upsyncPods,
	}
}

// Mutate applies the mutator changes to the object.
func (dm *PodSpecableMutator) Mutate(obj *unstructured.Unstructured) error {
	podTemplateUnstr, ok, err := unstructured.NestedMap(obj.UnstructuredContent(), "spec", "template")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("object should have a PodTemplate.Spec 'spec.template', but doesn't: %v", obj)
	}

	podTemplate := &corev1.PodTemplateSpec{}
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(
		podTemplateUnstr,
		&podTemplate)
	if err != nil {
		return err
	}
	upstreamLogicalName := logicalcluster.From(obj)

	desiredServiceAccountName := "default"
	if podTemplate.Spec.ServiceAccountName != "" && podTemplate.Spec.ServiceAccountName != "default" {
		desiredServiceAccountName = podTemplate.Spec.ServiceAccountName
	}

	rawSecretList, err := dm.listSecrets(upstreamLogicalName, obj.GetNamespace())
	if err != nil {
		return fmt.Errorf("error listing secrets for workspace %s: %w", upstreamLogicalName.String(), err)
	}

	secretList := make([]*unstructured.Unstructured, 0, len(rawSecretList))
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
		return fmt.Errorf("couldn't find a token upstream for the serviceaccount %s/%s in workspace %s", desiredServiceAccountName, obj.GetNamespace(), upstreamLogicalName.String())
	}

	// Setting AutomountServiceAccountToken to false allow us to control the ServiceAccount
	// VolumeMount and Volume definitions.
	podTemplate.Spec.AutomountServiceAccountToken = utilspointer.Bool(false)
	// Set to empty the serviceAccountName on podTemplate as we are not syncing the serviceAccount down to the workload cluster.
	podTemplate.Spec.ServiceAccountName = ""

	workspaceURL, err := dm.getWorkspaceURL(obj)
	if err != nil {
		return err
	}

	kcpExternalHost := workspaceURL.Hostname()
	kcpExternalPort := workspaceURL.Port()

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
				DefaultMode: utilspointer.Int32(420),
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
	for i := range podTemplate.Spec.Containers {
		for _, overrideEnv := range overrideEnvs {
			podTemplate.Spec.Containers[i].Env = updateEnv(podTemplate.Spec.Containers[i].Env, overrideEnv)
		}
		podTemplate.Spec.Containers[i].Env = resolveDownwardAPIFieldRefEnv(podTemplate.Spec.Containers[i].Env, obj)
		podTemplate.Spec.Containers[i].VolumeMounts = updateVolumeMount(podTemplate.Spec.Containers[i].VolumeMounts, serviceAccountMount)
	}

	// Override Envs, resolve downwardAPI FieldRef and add the VolumeMount to all the Init containers
	for i := range podTemplate.Spec.InitContainers {
		for _, overrideEnv := range overrideEnvs {
			podTemplate.Spec.InitContainers[i].Env = updateEnv(podTemplate.Spec.InitContainers[i].Env, overrideEnv)
		}
		podTemplate.Spec.InitContainers[i].Env = resolveDownwardAPIFieldRefEnv(podTemplate.Spec.InitContainers[i].Env, obj)
		podTemplate.Spec.InitContainers[i].VolumeMounts = updateVolumeMount(podTemplate.Spec.InitContainers[i].VolumeMounts, serviceAccountMount)
	}

	// Override Envs, resolve downwardAPI FieldRef and add the VolumeMount to all the Ephemeral containers
	for i := range podTemplate.Spec.EphemeralContainers {
		for _, overrideEnv := range overrideEnvs {
			podTemplate.Spec.EphemeralContainers[i].Env = updateEnv(podTemplate.Spec.EphemeralContainers[i].Env, overrideEnv)
		}
		podTemplate.Spec.EphemeralContainers[i].Env = resolveDownwardAPIFieldRefEnv(podTemplate.Spec.EphemeralContainers[i].Env, obj)
		podTemplate.Spec.EphemeralContainers[i].VolumeMounts = updateVolumeMount(podTemplate.Spec.EphemeralContainers[i].VolumeMounts, serviceAccountMount)
	}

	// Add the ServiceAccount volume with our overrides.
	found := false
	for i := range podTemplate.Spec.Volumes {
		if podTemplate.Spec.Volumes[i].Name == "kcp-api-access" {
			podTemplate.Spec.Volumes[i] = serviceAccountVolume
			found = true
		}
	}
	if !found {
		podTemplate.Spec.Volumes = append(podTemplate.Spec.Volumes, serviceAccountVolume)
	}

	// Overrides DNS to point to the workspace DNS
	dnsIP, err := dm.getDNSIPForWorkspace(upstreamLogicalName)
	if err != nil {
		// the DNS nameserver is not ready yet or other transient failure
		return err // retry
	}

	podTemplate.Spec.DNSPolicy = corev1.DNSNone
	podTemplate.Spec.DNSConfig = &corev1.PodDNSConfig{
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

	if dm.upsyncPods {
		syncTargetKey := workloadv1alpha1.ToSyncTargetKey(dm.syncTargetClusterName, dm.syncTargetName)
		labels := podTemplate.Labels
		if labels == nil {
			labels = map[string]string{}
		}
		labels[workloadv1alpha1.ClusterResourceStateLabelPrefix+syncTargetKey] = string(workloadv1alpha1.ResourceStateUpsync)
		labels[workloadv1alpha1.InternalDownstreamClusterLabel] = syncTargetKey
		podTemplate.Labels = labels

		// TODO(davidfestal): In the future we could add a diff annotation to transform the resource while upsyncing:
		//   - remove unnecessary fields we don't want leaking to upstream
		//   - add an owner reference to the upstream deployment
	}

	newPodTemplateUnstr, err := runtime.DefaultUnstructuredConverter.ToUnstructured(podTemplate)
	if err != nil {
		return err
	}

	// Set the changes back into the obj.
	return unstructured.SetNestedMap(obj.Object, newPodTemplateUnstr, "spec", "template")
}

func (dm *PodSpecableMutator) getDNSIPForWorkspace(workspace logicalcluster.Name) (string, error) {
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

// resolveDownwardAPIFieldRefEnv replaces the downwardAPI FieldRef EnvVars with the value from the deployment, right now it only replaces the metadata.namespace.
func resolveDownwardAPIFieldRefEnv(envs []corev1.EnvVar, podspecable *unstructured.Unstructured) []corev1.EnvVar {
	var result []corev1.EnvVar
	for _, env := range envs {
		if env.ValueFrom != nil && env.ValueFrom.FieldRef != nil && env.ValueFrom.FieldRef.FieldPath == "metadata.namespace" {
			result = append(result, corev1.EnvVar{
				Name:  env.Name,
				Value: podspecable.GetNamespace(),
			})
		} else {
			result = append(result, env)
		}
	}
	return result
}

// findEnv finds an env in a list of envs.
func findEnv(envs []corev1.EnvVar, name string) (bool, int) {
	for i := range envs {
		if envs[i].Name == name {
			return true, i
		}
	}
	return false, 0
}

// updateEnv updates an env from a list of envs.
func updateEnv(envs []corev1.EnvVar, overrideEnv corev1.EnvVar) []corev1.EnvVar {
	found, i := findEnv(envs, overrideEnv.Name)
	if found {
		envs[i].Value = overrideEnv.Value
	} else {
		envs = append(envs, overrideEnv)
	}

	return envs
}

// findVolumeMount finds a volume mount in a list of volume mounts.
func findVolumeMount(volumeMounts []corev1.VolumeMount, name string) (bool, int) {
	for i := range volumeMounts {
		if volumeMounts[i].Name == name {
			return true, i
		}
	}
	return false, 0
}

// updateVolumeMount updates a volume mount from a list of volume mounts.
func updateVolumeMount(volumeMounts []corev1.VolumeMount, overrideVolumeMount corev1.VolumeMount) []corev1.VolumeMount {
	found, i := findVolumeMount(volumeMounts, overrideVolumeMount.Name)
	if found {
		volumeMounts[i] = overrideVolumeMount
	} else {
		volumeMounts = append(volumeMounts, overrideVolumeMount)
	}

	return volumeMounts
}
