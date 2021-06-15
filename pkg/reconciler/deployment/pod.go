package deployment

import (
	"context"
	"fmt"
	"log"
	"math/rand"

	"github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	clusterlisters "github.com/kcp-dev/kcp/pkg/client/listers/cluster/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	corev1lister "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

var randInt func(int) int = rand.Intn

type podReconciler struct {
	kubeClient    kubernetes.Interface
	lister        corev1lister.PodLister
	clusterLister clusterlisters.ClusterLister
	indexer       cache.Indexer
}

func (c podReconciler) reconcile(ctx context.Context, pod *corev1.Pod) error {
	log.Println("reconciling", pod.Name)

	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}

	// If we're reconciling a root, make its current leafs match the
	// desired assigned state.
	if pod.Labels[clusterLabel] == "" {
		root := pod

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
			root.Status.Conditions = []corev1.PodCondition{{
				Type:    corev1.PodReady,
				Status:  corev1.ConditionFalse,
				Reason:  "NoRegisteredClusters",
				Message: "kcp has no clusters registered to receive Pods",
			}}
			// Delete all leafs.
			for _, l := range leafs {
				if err := c.kubeClient.CoreV1().Pods(root.Namespace).Delete(ctx, l.Name, metav1.DeleteOptions{}); err != nil {
					return err
				}
				log.Printf("deleted leaf %q", l.Name)
			}
			return nil
		}

		return c.applyLeafs(ctx, pod, leafs, cls)
	}

	// If we're reconciling a leaf, get all other leafs belonging to the
	// same root, and aggregate their status into the root's status.
	rootName := pod.Labels[ownedByLabel]
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
			Namespace:   pod.Namespace,
			Name:        rootName,
			ClusterName: pod.GetClusterName(),
		},
	})
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("root not found: %s", rootName)
	}
	root := rootIf.(*corev1.Pod)

	return c.aggregateStatus(ctx, root, leafs)
}

func (c podReconciler) applyLeafs(ctx context.Context, root *corev1.Pod, leafs []*corev1.Pod, cls []*v1alpha1.Cluster) error {
	switch len(leafs) {
	case 0: // Create a copy on some cluster (at random).

		cl := cls[randInt(len(cls))]

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
			APIVersion: "v1",
			Kind:       "Pod",
			UID:        root.UID,
			Name:       root.Name,
		}}

		// TODO: munge namespace
		vd.SetResourceVersion("")

		// Create a leaf.
		if _, err := c.kubeClient.CoreV1().Pods(root.Namespace).Create(ctx, vd, metav1.CreateOptions{}); err != nil {
			return err
		}

	case 1: // Update the existing copy.
		l := leafs[0].DeepCopy()
		l.Spec = root.Spec
		// TODO: update status?
		if _, err := c.kubeClient.CoreV1().Pods(root.Namespace).Update(ctx, l, metav1.UpdateOptions{}); err != nil {
			return err
		}
	default:
		// Uh oh.
	}

	return nil
}

// aggregateStatus aggregates the statuses of the leafs into the root object's status.
//
// Any changes to root are expected to be persisted outside this method's
// scope.
func (c podReconciler) aggregateStatus(ctx context.Context, root *corev1.Pod, leafs []*corev1.Pod) error {
	if len(leafs) != 1 {
		// uh oh.
	}

	// The status of the root is the status of the leaf.
	root.Status = leafs[0].Status

	// Update the root's status.
	_, err := c.kubeClient.CoreV1().Pods(root.Namespace).UpdateStatus(ctx, root, metav1.UpdateOptions{})
	return err
}
