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

package ingresssplitter

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/klog/v2"
)

const (
	clusterLabel     = "kcp.dev/cluster"
	toEnvoyLabel     = "ingress.kcp.dev/envoy"
	ownedByCluster   = "ingress.kcp.dev/owned-by-cluster"
	ownedByIngress   = "ingress.kcp.dev/owned-by-ingress"
	ownedByNamespace = "ingress.kcp.dev/owned-by-namespace"
)

// reconcile is triggered on every change to an ingress resource, or it's associated services (by tracker).
func (c *Controller) reconcile(ctx context.Context, ingress *networkingv1.Ingress) error {
	klog.InfoS("reconciling Ingress", "ClusterName", ingress.ClusterName, "Namespace", ingress.Namespace, "Name", ingress.Name)

	// If the Ingress has no clusterLabel, it's a Root Ingress, and we will need reconcile the leafs.
	if ingress.Labels[clusterLabel] == "" {
		// Create the selector based on the received ingress.
		ownedBySelector, err := createOwnedBySelector(ingress.ClusterName, ingress.Name, ingress.Namespace)
		if err != nil {
			return err
		}

		// Get the current Leaves of the ingress
		currentLeaves, err := c.ingressLister.List(ownedBySelector)
		if err != nil {
			klog.Errorf("failed to list leaves: %v", err)
			return nil
		}

		// Generate the desired leaves
		desiredLeaves, err := c.desiredLeaves(ctx, ingress)
		if err != nil {
			return err
		}

		// Clean the Ingress status field and mark the ingress not to be pushed to envoy config if there are no leaves.
		if len(desiredLeaves) == 0 {
			ingress.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{}
		}

		// Update the leafs and get missing ones to create and the ones be deleted.
		toCreate, toDelete, err := c.updateLeafs(ctx, currentLeaves, desiredLeaves)
		if err != nil {
			return err
		}

		// Create the new leaves
		for _, leaf := range toCreate {
			klog.InfoS("Creating leaf", "ClusterName", leaf.ClusterName, "Namespace", leaf.Namespace, "Name", leaf.Name)

			if _, err := c.client.Cluster(ingress.ClusterName).NetworkingV1().Ingresses(leaf.Namespace).Create(ctx, leaf, metav1.CreateOptions{}); err != nil {
				//TODO(jmprusi): Surface as user-facing condition.
				klog.Errorf("failed to create leaf: %v", err)
				return err
			}
		}

		// Delete the old leaves
		for _, leaf := range toDelete {
			klog.InfoS("Deleting leaf", "ClusterName", leaf.ClusterName, "Namespace", leaf.Namespace, "Name", leaf.Name)

			if err := c.client.Cluster(ingress.ClusterName).NetworkingV1().Ingresses(leaf.Namespace).Delete(ctx, leaf.Name, metav1.DeleteOptions{}); err != nil {
				//TODO(jmprusi): Surface as user-facing condition.
				klog.Errorf("failed to delete leaf: %v", err)
				return err
			}
		}

	} else {
		// If the ingress has the clusterLabel set, that means that it is a leaf and it's synced with
		// a cluster.

		// Create a selector based on the ingress labels, in order to find all the related leaves.
		ownedBySelector, err := createOwnedBySelector(ingress.Labels[ownedByCluster], ingress.Labels[ownedByIngress], ingress.Labels[ownedByNamespace])
		if err != nil {
			return err
		}

		// Get all the leaves
		others, err := c.ingressLister.List(ownedBySelector)
		if err != nil {
			return err
		}

		// Create the Root Ingress key and get it.
		ingressRootKey := rootIngressKeyFor(ingress)
		rootIf, exists, err := c.ingressIndexer.GetByKey(ingressRootKey)
		if err != nil {
			klog.Errorf("failed to get root ingress: %v", err)
			return nil
		}

		// TODO(jmprusi): A leaf without rootIngress? use OwnerRefs to avoid this.
		if !exists {
			//TODO(jmprusi): Add user-facing condition to leaf.
			klog.Errorf("root Ingress not found %s", ingressRootKey)
			return nil
		}

		// Deepcopy as we are going to modify the ingress and comes from a shared informer.
		rootIngress := rootIf.(*networkingv1.Ingress).DeepCopy()

		// Clean the current rootIngress, and then recreate it from the other leafs.
		rootIngress.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{}
		for _, o := range others {
			rootIngress.Status.LoadBalancer.Ingress = append(rootIngress.Status.LoadBalancer.Ingress, o.Status.LoadBalancer.Ingress...)
		}

		// If the envoy controlplane is enabled, we update the cache and generate and send to envoy a new snapshot.
		if c.envoycontrolplane != nil {
			// Generate the status hostname.
			statusHost := generateStatusHost(c.domain, rootIngress)
			// Now overwrite the Status of the rootIngress with our desired LB
			rootIngress.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{
				Hostname: statusHost,
			}}

			// Label the received ingress for envoy, as we want the controlplane to use this leaf
			// for updating the envoy config.
			ingress.Labels[toEnvoyLabel] = "true"
		}

		// Update the rootIngress status with our desired LB.
		// TODO(jmprusi): Use patch (safer) instead of update.
		if _, err := c.client.Cluster(rootIngress.ClusterName).NetworkingV1().Ingresses(rootIngress.Namespace).UpdateStatus(ctx, rootIngress, metav1.UpdateOptions{}); err != nil {
			klog.Errorf("failed to update root ingress status: %v", err)
			return err
		}
	}
	return nil
}

func (c *Controller) updateLeafs(ctx context.Context, currentLeaves []*networkingv1.Ingress, desiredLeaves []*networkingv1.Ingress) ([]*networkingv1.Ingress, []*networkingv1.Ingress, error) {
	var toDelete, toCreate []*networkingv1.Ingress

	for _, currentLeaf := range currentLeaves {
		found := false
		for _, desiredLeaf := range desiredLeaves {
			if desiredLeaf.GenerateName != currentLeaf.GenerateName || desiredLeaf.Labels[clusterLabel] != currentLeaf.Labels[clusterLabel] {
				continue
			}
			found = true

			if equality.Semantic.DeepEqual(currentLeaf.Spec, desiredLeaf.Spec) {
				klog.InfoS("Leaf is up to date", "ClusterName", currentLeaf.ClusterName, "Namespace", currentLeaf.Namespace, "Name", currentLeaf.Name)
				continue
			}

			klog.InfoS("Updating leaf", "ClusterName", currentLeaf.ClusterName, "Namespace", currentLeaf.Namespace, "Name", currentLeaf.Name)
			updated := currentLeaf.DeepCopy()
			updated.Spec = desiredLeaf.Spec
			if _, err := c.client.Cluster(currentLeaf.ClusterName).NetworkingV1().Ingresses(currentLeaf.Namespace).Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
				//TODO(jmprusi): Update root Ingress condition to reflect the error.
				return nil, nil, err
			}
			break
		}
		if !found {
			toDelete = append(toDelete, currentLeaf)
		}
	}

	for _, desiredLeaf := range desiredLeaves {
		found := false
		for _, currentLeaf := range currentLeaves {
			if desiredLeaf.GenerateName == currentLeaf.GenerateName && desiredLeaf.Labels[clusterLabel] == currentLeaf.Labels[clusterLabel] {
				found = true
				break
			}
		}

		if !found {
			toCreate = append(toCreate, desiredLeaf)
		}
	}

	return toCreate, toDelete, nil
}

// desiredLeaves returns a list of leaves (ingresses) to be created based on an ingress.
func (c *Controller) desiredLeaves(ctx context.Context, root *networkingv1.Ingress) ([]*networkingv1.Ingress, error) {
	// This will parse the ingresses and extract all the destination services,
	// then create a new ingress leaf for each of them.
	services, err := c.getServices(ctx, root)
	if err != nil {
		return nil, err
	}

	var clusterDests []string
	for _, service := range services {
		if service.Labels[clusterLabel] != "" {
			clusterDests = append(clusterDests, service.Labels[clusterLabel])
		} else {
			klog.Infof("Skipping service %q because it is not assigned to any cluster", service.Name)
		}

		// Trigger reconciliation of the root ingress when this service changes.
		c.tracker.add(root, service)
	}

	desiredLeaves := make([]*networkingv1.Ingress, 0, len(clusterDests))
	for _, cl := range clusterDests {
		vd := root.DeepCopy()
		vd.Name = ""

		vd.GenerateName = root.Name + "-"

		vd.Labels = map[string]string{}
		vd.Labels[clusterLabel] = cl

		// Label the leaf with the rootIngress information, so we can construct the ingress key
		// from it.
		vd.Labels[ownedByCluster] = root.ClusterName
		vd.Labels[ownedByIngress] = root.Name
		vd.Labels[ownedByNamespace] = root.Namespace

		// Cleanup all the other owner references.
		// TODO(jmprusi): Right now the syncer is syncing the OwnerReferences causing the ingresses to be deleted.
		vd.OwnerReferences = []metav1.OwnerReference{}
		vd.SetResourceVersion("")

		desiredLeaves = append(desiredLeaves, vd)
	}

	return desiredLeaves, nil
}

// TODO(jmprusi): Inline, or change to a more proper name, and review the hash algorithm.
func domainHashString(s string) string {
	h := fnv.New32a()
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

	//TODO(jmprusi): Hardcoded to the first one...
	if allRulesAreDomain {
		return ingress.Spec.Rules[0].Host
	}

	return domainHashString(ingress.Name+ingress.Namespace+ingress.ClusterName) + "." + domain
}

// getServices will parse the ingress object and return a list of the services.
func (c *Controller) getServices(ctx context.Context, ingress *networkingv1.Ingress) ([]*corev1.Service, error) {
	var services []*corev1.Service
	for _, rule := range ingress.Spec.Rules {
		for _, path := range rule.HTTP.Paths {
			// TODO(jmprusi): Use a service lister
			svc, err := c.client.Cluster(ingress.ClusterName).CoreV1().Services(ingress.Namespace).Get(ctx, path.Backend.Service.Name, metav1.GetOptions{})
			// TODO(jmprusi): If one of the services doesn't exist, we invalidate all the other ones.. review this.
			if err != nil {
				return nil, err
			}
			services = append(services, svc)
		}
	}
	return services, nil
}

func createOwnedBySelector(clustername, name, namespace string) (labels.Selector, error) {

	ownedClusterReq, err := labels.NewRequirement(ownedByCluster, selection.Equals, []string{clustername})
	if err != nil {
		return nil, err
	}
	ownedIngressReq, err := labels.NewRequirement(ownedByIngress, selection.Equals, []string{name})
	if err != nil {
		return nil, err
	}
	ownedNamespaceReq, err := labels.NewRequirement(ownedByNamespace, selection.Equals, []string{namespace})
	if err != nil {
		return nil, err
	}

	ownedBySelector := labels.NewSelector().Add(*ownedClusterReq, *ownedIngressReq, *ownedNamespaceReq)

	return ownedBySelector, nil
}
