package deployment

import (
	"context"
	"fmt"
	"log"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	clusterLabel = "cluster"
	ownedByLabel = "owned-by"
	pollInterval = time.Minute
)

func (c *Controller) reconcile(ctx context.Context, deployment *appsv1.Deployment) error {
	log.Println("reconciling deployment", deployment.Name)

	if deployment.Labels == nil || deployment.Labels[clusterLabel] == "" {
		// This is a root deployment; get its leafs.
		sel, err := labels.Parse(fmt.Sprintf("%s=%s", ownedByLabel, deployment.Labels[deployment.Name]))
		if err != nil {
			return err
		}
		leafs, err := c.lister.List(sel)
		if err != nil {
			return err
		}

		if len(leafs) == 0 {
			if err := c.createLeafs(ctx, deployment); err != nil {
				return err
			}
		}

	} else {
		// A leaf deployment was updated; get others and aggregate status.
		sel, err := labels.Parse(fmt.Sprintf("%s=%s", ownedByLabel, deployment.Labels[ownedByLabel]))
		if err != nil {
			return err
		}
		others, err := c.lister.List(sel)
		if err != nil {
			return err
		}

		// Aggregate .status from all leafs.
		deployment.Status.Replicas = 0
		deployment.Status.ReadyReplicas = 0
		deployment.Status.AvailableReplicas = 0
		deployment.Status.UnavailableReplicas = 0
		for _, o := range others {
			deployment.Status.Replicas += o.Status.Replicas
			deployment.Status.ReadyReplicas += o.Status.ReadyReplicas
			deployment.Status.AvailableReplicas += o.Status.AvailableReplicas
			deployment.Status.UnavailableReplicas += o.Status.UnavailableReplicas

		}

		// Cheat and set the root .status.conditions to the first leaf's .status.conditions.
		// TODO: do better.
		if len(others) > 0 {
			deployment.Status.Conditions = others[0].Status.Conditions
		}
	}

	return nil
}

func (c *Controller) createLeafs(ctx context.Context, root *appsv1.Deployment) error {
	cls, err := c.clusterLister.List(labels.Everything())
	if err != nil {
		return err
	}

	if len(cls) == 0 {
		root.Status.Conditions = []appsv1.DeploymentCondition{{
			Type:    appsv1.DeploymentProgressing,
			Status:  corev1.ConditionFalse,
			Reason:  "NoRegisteredClusters",
			Message: "kcp has no clusters registered to receive Deployments",
		}}
		return nil
	}

	if len(cls) == 1 {
		// nothing to split, just label Deployment for the only cluster.
		if root.Labels == nil {
			root.Labels = map[string]string{}
		}

		// TODO: munge cluster name
		root.Labels[clusterLabel] = cls[0].Name
		return nil
	}

	// If there are >1 Clusters, create a virtual Deployment labeled/named for each Cluster with a subset of replicas requested.
	// TODO: assign replicas unevenly based on load/scheduling.
	replicasEach := *root.Spec.Replicas / int32(len(cls))
	for _, cl := range cls {
		vd := root.DeepCopy()

		// TODO: munge cluster name
		vd.Name = fmt.Sprintf("%s--%s", root.Name, cl.Name)

		if vd.Labels == nil {
			vd.Labels = map[string]string{}
		}
		vd.Labels[clusterLabel] = cl.Name
		vd.Labels[ownedByLabel] = root.Name

		vd.Spec.Replicas = &replicasEach

		// Set OwnerReference so deleting the Deployment deletes all virtual deployments.
		vd.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
			UID:        root.UID,
			Name:       root.Name,
		}}

		// TODO: munge namespace
		vd.SetResourceVersion("")
		if _, err := c.kubeClient.AppsV1().Deployments(root.Namespace).Create(ctx, vd, metav1.CreateOptions{}); err != nil {
			return err
		}
		log.Printf("created child deployment %q", vd.Name)
	}

	return nil
}
