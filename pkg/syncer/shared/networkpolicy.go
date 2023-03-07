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

package shared

import (
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

// MakeAPIServerNetworkPolicyEgressRule creates a network policy egress rule to allow traffic to Kubernetes API Server
func MakeAPIServerNetworkPolicyEgressRule(ctx context.Context, kubeClient kubernetes.Interface) (*networkingv1.NetworkPolicyEgressRule, error) {
	var kubeEndpoints *corev1.Endpoints

	kubeEndpoints, err := kubeClient.CoreV1().Endpoints("default").Get(ctx, "kubernetes", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if len(kubeEndpoints.Subsets) == 0 || len(kubeEndpoints.Subsets[0].Addresses) == 0 {
		return nil, errors.New("missing kubernetes API endpoints")
	}

	subset := kubeEndpoints.Subsets[0]
	to := make([]networkingv1.NetworkPolicyPeer, len(subset.Addresses))
	for i, endpoint := range subset.Addresses {
		to[i] = networkingv1.NetworkPolicyPeer{
			IPBlock: &networkingv1.IPBlock{
				CIDR: endpoint.IP + "/32",
			},
		}
	}

	ports := make([]networkingv1.NetworkPolicyPort, len(subset.Ports))
	for i, port := range subset.Ports {
		pport := intstr.FromInt(int(port.Port))
		ports[i].Port = &pport
		pprotocol := port.Protocol
		ports[i].Protocol = &pprotocol
	}

	return &networkingv1.NetworkPolicyEgressRule{
		To:    to,
		Ports: ports,
	}, nil
}
