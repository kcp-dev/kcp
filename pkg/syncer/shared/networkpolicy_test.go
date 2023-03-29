/*
Copyright 2023 The KCP Authors.

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
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/pointer"
	"k8s.io/utils/semantic"
)

func TestMakeAPIServerNetworkPolicyEgressRule(t *testing.T) {
	tests := []struct {
		name     string
		objects  []runtime.Object
		wantErr  bool
		wantRule *networkingv1.NetworkPolicyEgressRule
	}{
		{
			name:    "no kube endpoint",
			wantErr: true,
		},
		{
			name: "kube endpoint with no subsets",
			objects: []runtime.Object{
				endpoints("kubernetes", "default", "", 0),
			},
			wantErr: true,
		},
		{
			name: "kube endpoint with at one subset, IP, no ports",
			objects: []runtime.Object{
				endpoints("kubernetes", "default", "8.8.8.8", 0),
			},
			wantRule: &networkingv1.NetworkPolicyEgressRule{
				To: []networkingv1.NetworkPolicyPeer{
					{
						IPBlock: &networkingv1.IPBlock{
							CIDR: "8.8.8.8/32",
						},
					},
				},
			},
		},
		{
			name: "kube endpoint with at one subset, IP, with ports",
			objects: []runtime.Object{
				endpoints("kubernetes", "default", "8.8.8.8", 80),
			},
			wantRule: &networkingv1.NetworkPolicyEgressRule{

				To: []networkingv1.NetworkPolicyPeer{
					{
						IPBlock: &networkingv1.IPBlock{
							CIDR: "8.8.8.8/32",
						},
					},
				},
				Ports: []networkingv1.NetworkPolicyPort{
					{
						Port:     &intstr.IntOrString{IntVal: 80},
						Protocol: (*corev1.Protocol)(pointer.StringPtr("TCP")),
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kubeClient := kubefake.NewSimpleClientset(tt.objects...)

			rule, err := MakeAPIServerNetworkPolicyEgressRule(context.Background(), kubeClient)

			if err == nil && tt.wantErr {
				t.Error("expected an error, got no error")
			}

			if err != nil {
				if !tt.wantErr {
					t.Errorf("expected no error, got %v", err)
				}
				return
			}
			if !semantic.EqualitiesOrDie().DeepEqual(tt.wantRule, rule) {
				t.Errorf("got = %v, want %v", rule, tt.wantRule)
			}
		})
	}
}

func endpoints(name, namespace, ip string, port int32) *corev1.Endpoints {
	endpoint := &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	if ip != "" {
		endpoint.Subsets = []corev1.EndpointSubset{{Addresses: []corev1.EndpointAddress{{IP: ip}}}}

		if port != 0 {
			endpoint.Subsets[0].Ports = []corev1.EndpointPort{{Port: port, Protocol: corev1.ProtocolTCP}}
		}
	}

	return endpoint
}
