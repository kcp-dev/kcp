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

package ingress

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/klog/v2"

	envoycontrolplane "github.com/kcp-dev/kcp/pkg/localenvoy/controlplane"
)

const (
	clusterLabel = "kcp.dev/cluster"
)

// reconcile is triggered on every change to an ingress resource.
func (c *Controller) reconcile(ctx context.Context, ingress *networkingv1.Ingress) error {
	klog.InfoS("reconciling Ingress", "ClusterName", ingress.ClusterName, "Namespace", ingress.Namespace, "Name", ingress.Name)

	if ingress.Labels[clusterLabel] == "" {
		// Root
		if len(ingress.Status.LoadBalancer.Ingress) > 0 && ingress.Status.LoadBalancer.Ingress[0].Hostname != "" {
			// Already set - never changes - do nothing
			return nil
		}

		ingress.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{
			Hostname: generateStatusHost(c.domain, ingress),
		}}

		return nil
	}

	// Leaf:

	// If the Ingress has no status, that means that the ingress controller on the pcluster has
	// not picked up this leaf yet, so we should skip it.
	if len(ingress.Status.LoadBalancer.Ingress) == 0 {
		klog.Infof("Ingress %s - %s/%s has no loadbalancer status set, skipping.", ingress.ClusterName, ingress.Namespace, ingress.Name)
		return nil
	}

	// Label the received ingress for envoy, as we want the controlplane to use this leaf
	// for updating the envoy config.
	ingress.Labels[envoycontrolplane.ToEnvoyLabel] = "true"

	return nil
}

// TODO(jmprusi): Review the hash algorithm.
func domainHashString(s string) string {
	h := fnv.New32a()
	// nolint: errcheck
	h.Write([]byte(s))
	return fmt.Sprint(h.Sum32())
}

// generateStatusHost returns a string that represent the desired status hostname for the ingress.
// If the host is part of the same domain, it will be preserved as the status hostname, if not
// a new one will be generated based on a hash of the ingress name, namespace and clusterName.
func generateStatusHost(domain string, ingress *networkingv1.Ingress) string {
	// TODO(jmprusi): using "contains" is a bad idea as it could be abused by crafting a malicious hostname, but for a PoC it should be good enough?
	allRulesAreDomain := true
	for _, rule := range ingress.Spec.Rules {
		if !strings.Contains(rule.Host, domain) {
			allRulesAreDomain = false
			break
		}
	}

	// TODO(jmprusi): Hardcoded to the first one...
	if allRulesAreDomain {
		return ingress.Spec.Rules[0].Host
	}

	return domainHashString(ingress.Name+ingress.Namespace+ingress.ClusterName) + "." + domain
}
