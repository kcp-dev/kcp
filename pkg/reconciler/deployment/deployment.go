package deployment

import (
	"context"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog"
)

const (
	clusterLabel = "kcp.dev/cluster"
	ownedByLabel = "kcp.dev/owned-by"
	pollInterval = time.Minute
)

func (c *Controller) reconcile(ctx context.Context, deployment *appsv1.Deployment) error {
	klog.Infof("reconciling deployment %q", deployment.Name)

	if deployment.Labels == nil {
		deployment.Labels = map[string]string{}
	}

	// If we're reconciling a root, make its current leafs match the
	// desired split state.
	if deployment.Labels[clusterLabel] == "" {
		root := deployment

		// Get all leafs belonging to the root.
		sel, err := labels.Parse(fmt.Sprintf("%s=%s", ownedByLabel, root.Name))
		if err != nil {
			return err
		}
		leafs, err := c.lister.List(sel)
		if err != nil {
			return err
		}

		// Get all clusters.
		cls, err := c.clusterLister.List(labels.Everything())
		if err != nil {
			return err
		}
		// Filter out un-ready clusters.
		cls = filterClusters(cls)

		// If there's no viable clusters, the root cannot progress.
		if len(cls) == 0 {
			root.Status.Conditions = []appsv1.DeploymentCondition{{
				Type:    appsv1.DeploymentProgressing,
				Status:  corev1.ConditionFalse,
				Reason:  "NoRegisteredClusters",
				Message: "kcp has no clusters registered to receive Deployments",
			}}
			// Delete all leafs.
			for _, l := range leafs {
				if err := c.kubeClient.AppsV1().Deployments(root.Namespace).Delete(ctx, l.Name, metav1.DeleteOptions{}); err != nil {
					return err
				}
				log.Printf("deleted leaf %q", l.Name)
			}
			return nil
		}

		return c.applyLeafs(ctx, root, leafs, cls)
	}

	// If we're reconciling a leaf, get all other leafs belonging to the
	// same root, and aggregate their status into the root's status.
	rootName := deployment.Labels[ownedByLabel]
	sel, err := labels.Parse(fmt.Sprintf("%s=%s", ownedByLabel, rootName))
	if err != nil {
		return err
	}
	leafs, err := c.lister.List(sel)
	if err != nil {
		return err
	}

	// Get the root.
	rootIf, exists, err := c.indexer.Get(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   deployment.Namespace,
			Name:        rootName,
			ClusterName: deployment.GetClusterName(),
		},
	})
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("root deployment not found: %s", rootName)
	}
	root := rootIf.(*appsv1.Deployment)

	return c.aggregateStatus(ctx, root, leafs)
}

// filterClusters filters Cluster objects and only returns those that are
// Ready.
//
// TODO: consider other cluster traits here, like scheduling constraints.
func filterClusters(cls []*v1alpha1.Cluster) []*v1alpha1.Cluster {
	var out []*v1alpha1.Cluster
	for _, cl := range cls {
		if cl.IsReady() {
			out = append(out, cl)
		}
	}
	// Sort for reproducibility.
	// TODO: This biases more replicas toward lexigraphically earlier named
	// clusters, come up with something else that's better and stable.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// applyLeafs applies the desired state of leaf deployments, based on the
// number of ready clusters and desired root deployment replicas.
//
// Leaf deployments will be:
// - created for clusters that don't already have a leaf.
// - updated for clusters that already have a leaf (e.g., to take into account
//   new replica counts assigned to that leaf, as other leafs are
//   created/deleted)
// - deleted if they are for clusters that are deleted or become un-ready
//
// Any changes to root are expected to be persisted outside this method's
// scope.
func (c *Controller) applyLeafs(ctx context.Context, root *appsv1.Deployment, leafs []*appsv1.Deployment, cls []*v1alpha1.Cluster) error {
	// currentLeafs indexes existing leaf deployments by name, and stores
	// whether that leaf was updated during this reconciliation.
	// Any leafs that aren't updated are assumed to be orphaned, and will
	// be deleted (e.g., they belong to an un-ready cluster).
	currentLeafs := map[string]bool{}
	for _, l := range leafs {
		currentLeafs[l.Name] = false
	}

	// Reassign replicas across available deployments.
	// Try to make this as even as possible, so that no cluster as 2+ more
	// replicas than any other.
	each := make([]int32, len(cls))
	for i := 0; i < int(*root.Spec.Replicas); i++ {
		each[i%len(cls)]++
	}

	for idx, cl := range cls {
		vd := root.DeepCopy()

		// TODO: munge cluster name
		vd.Name = fmt.Sprintf("%s--%s", root.Name, cl.Name)
		if vd.Labels == nil {
			vd.Labels = map[string]string{}
		}
		vd.Labels[clusterLabel] = cl.Name
		vd.Labels[ownedByLabel] = root.Name

		// Set replica count.
		vd.Spec.Replicas = &each[idx]

		// Set OwnerReference so deleting the Deployment deletes all virtual deployments.
		vd.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
			UID:        root.UID,
			Name:       root.Name,
		}}

		// TODO: munge namespace
		vd.SetResourceVersion("")

		if _, found := currentLeafs[vd.Name]; found {
			// Update a leaf.
			if _, err := c.kubeClient.AppsV1().Deployments(root.Namespace).Update(ctx, vd, metav1.UpdateOptions{}); err != nil {
				return err
			}
			// This leaf was updated, and should not be deleted.
			currentLeafs[vd.Name] = true
			klog.Infof("updated child deployment %q", vd.Name)
		} else {
			// Create a leaf.
			// This handles the N -> N+1 scale-up scenario.
			if _, err := c.kubeClient.AppsV1().Deployments(root.Namespace).Create(ctx, vd, metav1.CreateOptions{}); err != nil {
				return err
			}
			klog.Infof("created child deployment %q", vd.Name)
		}
	}

	for n, updated := range currentLeafs {
		if !updated {
			// Delete any other leafs that we didn't update; these
			// might have belonged to deleted/un-ready clusters.
			// This handles the N -> N-1 scale-down scenario.
			if err := c.kubeClient.AppsV1().Deployments(root.Namespace).Delete(ctx, n, metav1.DeleteOptions{}); err != nil {
				return err
			}
			klog.Infof("deleted child deployment %q", n)
		}
	}

	return nil
}

// aggregateStatus aggregates the statuses of the leafs into the root object's status.
//
// Any changes to root are expected to be persisted outside this method's
// scope.
func (c Controller) aggregateStatus(ctx context.Context, root *appsv1.Deployment, leafs []*appsv1.Deployment) error {
	// Aggregate .status from all leafs.
	root.Status.Replicas = 0
	root.Status.UpdatedReplicas = 0
	root.Status.ReadyReplicas = 0
	root.Status.AvailableReplicas = 0
	root.Status.UnavailableReplicas = 0
	for _, o := range leafs {
		root.Status.Replicas += o.Status.Replicas
		root.Status.UpdatedReplicas += o.Status.UpdatedReplicas
		root.Status.ReadyReplicas += o.Status.ReadyReplicas
		root.Status.AvailableReplicas += o.Status.AvailableReplicas
		root.Status.UnavailableReplicas += o.Status.UnavailableReplicas
	}

	// Cheat and set the root .status.conditions to the first leaf's .status.conditions.
	// TODO: do better.
	if len(leafs) > 0 {
		root.Status.Conditions = leafs[0].Status.Conditions
	}

	// Update the root's status.
	_, err := c.kubeClient.AppsV1().Deployments(root.Namespace).UpdateStatus(ctx, root, metav1.UpdateOptions{})
	return err
}
