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

package dns

import (
	"bytes"
	"embed"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/yaml"

	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

//go:embed *.yaml
var dnsFiles embed.FS

var (
	serviceAccountTemplate corev1.ServiceAccount
	roleTemplate           rbacv1.Role
	roleBindingTemplate    rbacv1.RoleBinding
	deploymentTemplate     appsv1.Deployment
	serviceTemplate        corev1.Service
	networkPolicyTemplate  networkingv1.NetworkPolicy
)

func init() {
	loadTemplateOrDie("serviceaccount_dns.yaml", &serviceAccountTemplate)
	loadTemplateOrDie("role_dns.yaml", &roleTemplate)
	loadTemplateOrDie("rolebinding_dns.yaml", &roleBindingTemplate)
	loadTemplateOrDie("deployment_dns.yaml", &deploymentTemplate)
	loadTemplateOrDie("service_dns.yaml", &serviceTemplate)
	loadTemplateOrDie("networkpolicy_dns.yaml", &networkPolicyTemplate)
}

func MakeServiceAccount(name, namespace string) *corev1.ServiceAccount {
	sa := serviceAccountTemplate.DeepCopy()

	sa.Name = name
	sa.Namespace = namespace

	return sa
}

func MakeRole(name, namespace string) *rbacv1.Role {
	role := roleTemplate.DeepCopy()

	role.Name = name
	role.Namespace = namespace
	role.Rules[0].ResourceNames[0] = name

	return role
}

func MakeRoleBinding(name, namespace string) *rbacv1.RoleBinding {
	roleBinding := roleBindingTemplate.DeepCopy()

	roleBinding.Name = name
	roleBinding.Namespace = namespace
	roleBinding.RoleRef.Name = name
	roleBinding.Subjects[0].Name = name
	roleBinding.Subjects[0].Namespace = namespace

	return roleBinding
}

func MakeDeployment(name, namespace, image string) *appsv1.Deployment {
	deployment := deploymentTemplate.DeepCopy()

	deployment.Name = name
	deployment.Namespace = namespace
	deployment.Spec.Selector.MatchLabels["app"] = name
	deployment.Spec.Template.Labels["app"] = name
	deployment.Spec.Template.Spec.Containers[0].Image = image
	deployment.Spec.Template.Spec.Containers[0].Args[3] = name
	deployment.Spec.Template.Spec.ServiceAccountName = name

	return deployment
}

func MakeService(name, namespace string) *corev1.Service {
	service := serviceTemplate.DeepCopy()

	service.Name = name
	service.Namespace = namespace
	service.Labels["app"] = name
	service.Spec.Selector["app"] = name

	return service
}

func MakeNetworkPolicy(name, namespace, tenantID string, kubeEndpoints *corev1.EndpointSubset) *networkingv1.NetworkPolicy {
	np := networkPolicyTemplate.DeepCopy()

	np.Name = name
	np.Namespace = namespace
	np.Spec.PodSelector.MatchLabels["app"] = name
	np.Spec.Ingress[0].From[0].NamespaceSelector.MatchLabels[shared.TenantIDLabel] = tenantID

	to := make([]networkingv1.NetworkPolicyPeer, len(kubeEndpoints.Addresses))
	for i, endpoint := range kubeEndpoints.Addresses {
		to[i] = networkingv1.NetworkPolicyPeer{
			IPBlock: &networkingv1.IPBlock{
				CIDR: endpoint.IP + "/32",
			},
		}
	}
	np.Spec.Egress[1].To = to

	ports := make([]networkingv1.NetworkPolicyPort, len(kubeEndpoints.Ports))
	for i, port := range kubeEndpoints.Ports {
		pport := intstr.FromInt(int(port.Port))
		ports[i].Port = &pport
		pprotocol := port.Protocol
		ports[i].Protocol = &pprotocol
	}
	np.Spec.Egress[1].Ports = ports

	return np
}

// load a YAML resource into a typed kubernetes object.
func loadTemplateOrDie(filename string, obj interface{}) {
	raw, err := dnsFiles.ReadFile(filename)
	if err != nil {
		panic(fmt.Sprintf("failed to read file: %v", err))
	}
	decoder := yaml.NewYAMLToJSONDecoder(bytes.NewReader(raw))

	var u unstructured.Unstructured
	err = decoder.Decode(&u)
	if err != nil {
		panic(fmt.Sprintf("failed to decode file: %v", err))
	}

	err = runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, obj)
	if err != nil {
		panic(fmt.Sprintf("failed to convert object: %v", err))
	}
}
