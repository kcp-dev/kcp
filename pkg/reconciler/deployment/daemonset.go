package deployment

import (
	"context"
	"fmt"
	"log"

	"github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	clusterlisters "github.com/kcp-dev/kcp/pkg/client/listers/cluster/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	appsv1lister "k8s.io/client-go/listers/apps/v1"
)

type daemonSetReconciler struct {
	kubeClient    kubernetes.Interface
	lister        appsv1lister.DaemonSetLister
	clusterLister clusterlisters.ClusterLister
}

func (c daemonSetReconciler) reconcile(ctx context.Context, daemonSet *appsv1.DaemonSet) error {
	log.Println("reconciling", daemonSet.Name)

	if daemonSet.Labels == nil {
		daemonSet.Labels = map[string]string{}
	}

	// If we're reconciling a root, make its current leafs match the
	// desired replicated state.
	if daemonSet.Labels[clusterLabel] == "" {
		root := daemonSet

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
			root.Status.Conditions = []appsv1.DaemonSetCondition{{
				Type:    appsv1.DaemonSetConditionType("Ready"),
				Status:  corev1.ConditionFalse,
				Reason:  "NoRegisteredClusters",
				Message: "kcp has no clusters registered to receive DaemonSets",
			}}
			// Delete all leafs.
			for _, l := range leafs {
				if err := c.kubeClient.AppsV1().DaemonSets(root.Namespace).Delete(ctx, l.Name, metav1.DeleteOptions{}); err != nil {
					return err
				}
				log.Printf("deleted leaf %q", l.Name)
			}
			return nil
		}

		return c.applyLeafs(ctx, daemonSet, leafs, cls)
	}

	// TODO: aggregate status.
	return nil
}

func (c daemonSetReconciler) applyLeafs(ctx context.Context, root *appsv1.DaemonSet, leafs []*appsv1.DaemonSet, cls []*v1alpha1.Cluster) error {
	// currentLeafs indexes existing leafs by name, and stores
	// whether that leaf was updated during this reconciliation.
	// Any leafs that aren't updated are assumed to be orphaned, and will
	// be deleted (e.g., they belong to an un-ready cluster).
	currentLeafs := map[string]bool{}
	for _, l := range leafs {
		currentLeafs[l.Name] = false
	}

	// Create a DaemonSet for each cluster.
	for _, cl := range cls {
		vd := root.DeepCopy()

		// TODO: munge cluster name
		vd.Name = fmt.Sprintf("%s--%s", root.Name, cl.Name)
		if vd.Labels == nil {
			vd.Labels = map[string]string{}
		}
		vd.Labels[clusterLabel] = cl.Name
		vd.Labels[ownedByLabel] = root.Name

		// Set OwnerReference so deleting the root deletes all leafs.
		vd.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: "apps/v1",
			Kind:       "DaemonSet",
			UID:        root.UID,
			Name:       root.Name,
		}}

		// TODO: munge namespace
		vd.SetResourceVersion("")

		if _, found := currentLeafs[vd.Name]; found {
			// Update a leaf.
			if _, err := c.kubeClient.AppsV1().DaemonSets(root.Namespace).Update(ctx, vd, metav1.UpdateOptions{}); err != nil {
				return err
			}
			// This leaf was updated, and should not be deleted.
			currentLeafs[vd.Name] = true
			log.Printf("updated leaf %q", vd.Name)
		} else {
			// Create a leaf.
			// This handles the N -> N+1 scale-up scenario.
			if _, err := c.kubeClient.AppsV1().DaemonSets(root.Namespace).Create(ctx, vd, metav1.CreateOptions{}); err != nil {
				return err
			}
			log.Printf("created leaf %q", vd.Name)
		}
	}

	for n, updated := range currentLeafs {
		if !updated {
			// Delete any other leafs that we didn't update; these
			// might have belonged to deleted/un-ready clusters.
			// This handles the N -> N-1 scale-down scenario.
			if err := c.kubeClient.AppsV1().DaemonSets(root.Namespace).Delete(ctx, n, metav1.DeleteOptions{}); err != nil {
				return err
			}
			log.Printf("deleted leaf %q", n)
		}
	}

	return nil
}

// aggregateStatus aggregates the statuses of the leafs into the root object's status.
//
// Any changes to root are expected to be persisted outside this method's
// scope.
func (c daemonSetReconciler) aggregateStatus(ctx context.Context, root *appsv1.DaemonSet, leafs []*appsv1.DaemonSet) error {
	// Cheat and set the root .status.conditions to the first leaf's .status.conditions.
	// TODO: do better.
	if len(leafs) > 0 {
		root.Status.Conditions = leafs[0].Status.Conditions
	}

	// Update the root's status.
	_, err := c.kubeClient.AppsV1().DaemonSets(root.Namespace).UpdateStatus(ctx, root, metav1.UpdateOptions{})
	return err
}
