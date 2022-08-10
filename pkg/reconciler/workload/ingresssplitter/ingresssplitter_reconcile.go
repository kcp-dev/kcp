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
	"strings"

	"github.com/kcp-dev/logicalcluster/v2"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

const (
	OwnedByCluster   = "ingress.kcp.dev/owned-by-cluster"
	OwnedByIngress   = "ingress.kcp.dev/owned-by-ingress"
	OwnedByNamespace = "ingress.kcp.dev/owned-by-namespace"
)

// reconcile is triggered on every change to an ingress resource, or it's associated services (by tracker).
func (c *Controller) reconcile(ctx context.Context, ingress *networkingv1.Ingress) error {
	logger := klog.FromContext(ctx)
	logger.Info("reconciling Ingress")

	//nolint:staticcheck
	if shared.DeprecatedGetAssignedSyncTarget(ingress.Labels) == "" {
		// we have a root ingress here
		if err := c.reconcileLeaves(ctx, ingress); err != nil {
			return err
		}
	} else if c.aggregateLeavesStatus {
		// we have a leave ingress here and have to reconcile the root status
		if err := c.reconcileRootStatusFromLeaves(ctx, ingress); err != nil {
			return err
		}
	}

	return nil
}

func (c *Controller) reconcileLeaves(ctx context.Context, ingress *networkingv1.Ingress) error {
	logger := klog.FromContext(ctx)
	ingressClusterName := logicalcluster.From(ingress)
	ownedByRootIngressSelector, err := createOwnedBySelector(ingressClusterName, ingress.Name, ingress.Namespace)
	if err != nil {
		return err
	}
	currentLeaves, err := c.ingressLister.List(ownedByRootIngressSelector)
	if err != nil {
		logger.Error(err, "failed to list leaves")
		return nil
	}

	// Generate the desired leaves
	desiredLeaves, err := c.desiredLeaves(ctx, ingress)
	if err != nil {
		return err
	}

	// Update the leafs and get missing ones to create and the ones be deleted.
	toCreate, toDelete, err := c.updateLeafs(ctx, currentLeaves, desiredLeaves)
	if err != nil {
		return err
	}

	// Create the new leaves
	for _, leaf := range toCreate {
		logger = logger.WithValues(logging.FromPrefix("leafIngress", leaf)...)
		logger.Info("creating leaf")

		if _, err := c.client.NetworkingV1().Ingresses(leaf.Namespace).Create(logicalcluster.WithCluster(ctx, ingressClusterName), leaf, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			// TODO(jmprusi): Surface as user-facing condition.
			return fmt.Errorf("failed to create leaf: %w", err)
		}
	}

	// Delete the old leaves
	for _, leaf := range toDelete {
		logger = logger.WithValues(logging.FromPrefix("leafIngress", leaf)...)
		logger.Info("deleting leaf")

		if err := c.client.NetworkingV1().Ingresses(leaf.Namespace).Delete(logicalcluster.WithCluster(ctx, ingressClusterName), leaf.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			// TODO(jmprusi): Surface as user-facing condition.
			return fmt.Errorf("failed to delete leaf: %w", err)
		}
	}

	return nil
}

func (c *Controller) reconcileRootStatusFromLeaves(ctx context.Context, ingress *networkingv1.Ingress) error {
	logger := klog.FromContext(ctx)
	// Create a selector based on the ingress labels, in order to find all the related leaves.
	ownedBySelector, err := createOwnedBySelector(UnescapeClusterNameLabel(ingress.Labels[OwnedByCluster]), ingress.Labels[OwnedByIngress], ingress.Labels[OwnedByNamespace])
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
		logger.Error(err, "failed to get root Ingress")
		return nil
	}

	// TODO(jmprusi): A leaf without rootIngress? use OwnerRefs to avoid this.
	if !exists {
		// TODO(jmprusi): Add user-facing condition to leaf.
		logger.Info("root Ingress not found")
		return nil
	}

	// Clean the current rootIngress, and then recreate it from the other leafs.
	rootIngress := rootIf.(*networkingv1.Ingress).DeepCopy()
	rootIngress.Status.LoadBalancer.Ingress = make([]corev1.LoadBalancerIngress, 0, len(others))
	for _, o := range others {
		rootIngress.Status.LoadBalancer.Ingress = append(rootIngress.Status.LoadBalancer.Ingress, o.Status.LoadBalancer.Ingress...)
	}

	// Update the rootIngress status with our desired LB.
	// TODO(jmprusi): Use patch (safer) instead of update.
	if _, err := c.client.NetworkingV1().Ingresses(rootIngress.Namespace).UpdateStatus(logicalcluster.WithCluster(ctx, logicalcluster.From(rootIngress)), rootIngress, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update root ingress status: %w", err)
	}

	return nil
}

func (c *Controller) updateLeafs(ctx context.Context, currentLeaves []*networkingv1.Ingress, desiredLeaves []*networkingv1.Ingress) ([]*networkingv1.Ingress, []*networkingv1.Ingress, error) {
	logger := klog.FromContext(ctx)
	var toDelete, toCreate []*networkingv1.Ingress

	for _, currentLeaf := range currentLeaves {
		found := false
		logger = logger.WithValues(logging.FromPrefix("leafIngress", currentLeaf)...)
		for _, desiredLeaf := range desiredLeaves {
			//nolint:staticcheck
			if desiredLeaf.Name != currentLeaf.Name || shared.DeprecatedGetAssignedSyncTarget(desiredLeaf.Labels) != shared.DeprecatedGetAssignedSyncTarget(currentLeaf.Labels) {
				continue
			}
			found = true

			if equality.Semantic.DeepEqual(currentLeaf.Spec, desiredLeaf.Spec) {
				logger.Info("leaf is up to date")
				continue
			}

			logger.Info("updating leaf")
			updated := currentLeaf.DeepCopy()
			updated.Spec = desiredLeaf.Spec
			if _, err := c.client.NetworkingV1().Ingresses(currentLeaf.Namespace).Update(logicalcluster.WithCluster(ctx, logicalcluster.From(currentLeaf)), updated, metav1.UpdateOptions{}); err != nil {
				// TODO(jmprusi): Update root Ingress condition to reflect the error.
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
			//nolint:staticcheck
			if desiredLeaf.Name == currentLeaf.Name && shared.DeprecatedGetAssignedSyncTarget(desiredLeaf.Labels) == shared.DeprecatedGetAssignedSyncTarget(currentLeaf.Labels) {
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
	logger := klog.FromContext(ctx)
	// This will parse the ingresses and extract all the destination services,
	// then create a new ingress leaf for each of them.
	services, err := c.getServices(ctx, root)
	if err != nil {
		return nil, err
	}

	var clusterDests []string
	for _, service := range services {
		//nolint:staticcheck
		if shared.DeprecatedGetAssignedSyncTarget(service.Labels) != "" {
			clusterDests = append(clusterDests, shared.DeprecatedGetAssignedSyncTarget(service.Labels))
		} else {
			logging.WithObject(logger, service).Info("skipping Service because it is not assigned to any cluster")
		}

		// Trigger reconciliation of the root ingress when this service changes.
		c.tracker.add(ctx, root, service)
	}

	desiredLeaves := make([]*networkingv1.Ingress, 0, len(clusterDests))
	for _, cl := range clusterDests {
		vd := root.DeepCopy()
		vd.Name = root.Name + "-" + cl

		vd.Labels = map[string]string{}
		vd.Labels[workloadv1alpha1.ClusterResourceStateLabelPrefix+cl] = string(workloadv1alpha1.ResourceStateSync)

		// Label the leaf with the rootIngress information, so we can construct the ingress key
		// from it.
		vd.Labels[OwnedByCluster] = LabelEscapeClusterName(logicalcluster.From(root))
		vd.Labels[OwnedByIngress] = root.Name
		vd.Labels[OwnedByNamespace] = root.Namespace

		// Cleanup all the other owner references.
		// TODO(jmprusi): Right now the syncer is syncing the OwnerReferences causing the ingresses to be deleted.
		vd.OwnerReferences = []metav1.OwnerReference{}
		vd.SetResourceVersion("")

		desiredLeaves = append(desiredLeaves, vd)
	}

	return desiredLeaves, nil
}

// getServices will parse the ingress object and return a list of the services.
func (c *Controller) getServices(ctx context.Context, ingress *networkingv1.Ingress) ([]*corev1.Service, error) {
	var services []*corev1.Service
	for _, rule := range ingress.Spec.Rules {
		for _, path := range rule.HTTP.Paths {
			// TODO(jmprusi): Use a service lister
			svc, err := c.client.CoreV1().Services(ingress.Namespace).Get(logicalcluster.WithCluster(ctx, logicalcluster.From(ingress)), path.Backend.Service.Name, metav1.GetOptions{})
			// TODO(jmprusi): If one of the services doesn't exist, we invalidate all the other ones.. review this.
			if err != nil {
				return nil, err
			}
			services = append(services, svc)
		}
	}
	return services, nil
}

func createOwnedBySelector(clusterName logicalcluster.Name, name, namespace string) (labels.Selector, error) {
	ownedClusterReq, err := labels.NewRequirement(OwnedByCluster, selection.Equals, []string{LabelEscapeClusterName(clusterName)})
	if err != nil {
		return nil, err
	}
	ownedIngressReq, err := labels.NewRequirement(OwnedByIngress, selection.Equals, []string{name})
	if err != nil {
		return nil, err
	}
	ownedNamespaceReq, err := labels.NewRequirement(OwnedByNamespace, selection.Equals, []string{namespace})
	if err != nil {
		return nil, err
	}

	ownedBySelector := labels.NewSelector().Add(*ownedClusterReq, *ownedIngressReq, *ownedNamespaceReq)

	return ownedBySelector, nil
}

func LabelEscapeClusterName(n logicalcluster.Name) string {
	return strings.ReplaceAll(n.String(), ":", "_")
}

func UnescapeClusterNameLabel(s string) logicalcluster.Name {
	return logicalcluster.New(strings.ReplaceAll(s, "_", ":"))
}
