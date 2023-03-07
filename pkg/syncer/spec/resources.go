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

package spec

import (
	"bytes"
	"embed"
	"fmt"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

//go:embed *.yaml
var networkPoliciesFiles embed.FS

var (
	tenantNetworkPolicyTemplate networkingv1.NetworkPolicy
)

func init() {
	loadTemplateOrDie("networkpolicy_tenant.yaml", &tenantNetworkPolicyTemplate)
}

func MakeTenantNetworkPolicy(namespace, tenantID, syncTargetKey string, apiServerRule *networkingv1.NetworkPolicyEgressRule) *networkingv1.NetworkPolicy {
	np := tenantNetworkPolicyTemplate.DeepCopy()

	np.Namespace = namespace
	np.Spec.Ingress[0].From[0].NamespaceSelector.MatchLabels[shared.TenantIDLabel] = tenantID

	np.Spec.Egress[0].To[0].NamespaceSelector.MatchLabels[shared.TenantIDLabel] = tenantID

	np.Spec.Egress[2].To[0].NamespaceSelector.MatchLabels[workloadv1alpha1.InternalDownstreamClusterLabel] = syncTargetKey
	np.Spec.Egress[2].To[0].PodSelector.MatchLabels[shared.TenantIDLabel] = tenantID

	np.Spec.Egress[3] = *apiServerRule

	return np
}

// load a YAML resource into a typed kubernetes object.
func loadTemplateOrDie(filename string, obj interface{}) {
	raw, err := networkPoliciesFiles.ReadFile(filename)
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
