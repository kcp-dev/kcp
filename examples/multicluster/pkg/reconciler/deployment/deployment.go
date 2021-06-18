package deployment

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"
)

const (
	clusterLabel = "kcp.dev/cluster"
	ownedByLabel = "kcp.dev/owned-by"
	pollInterval = time.Minute
)

func (c *Controller) reconcile(ctx context.Context, deployment *appsv1.Deployment) error {
	klog.Infof("reconciling deployment %q", deployment.Name)

	if deployment.Labels == nil || deployment.Labels[clusterLabel] == "" {
		// This is a root deployment; get its leafs.
		sel, err := labels.Parse(fmt.Sprintf("%s=%s", ownedByLabel, deployment.Name))
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
		rootDeploymentName := deployment.Labels[ownedByLabel]
		// A leaf deployment was updated; get others and aggregate status.
		sel, err := labels.Parse(fmt.Sprintf("%s=%s", ownedByLabel, rootDeploymentName))
		if err != nil {
			return err
		}
		others, err := c.lister.List(sel)
		if err != nil {
			return err
		}

		var rootDeployment *appsv1.Deployment

		rootIf, exists, err := c.indexer.Get(&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:   deployment.Namespace,
				Name:        rootDeploymentName,
				ClusterName: deployment.GetClusterName(),
			},
		})
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("Root deployment not found: %s", rootDeploymentName)
		}

		rootDeployment = rootIf.(*appsv1.Deployment)

		// Aggregate .status from all leafs.

		rootDeployment = rootDeployment.DeepCopy()
		rootDeployment.Status.Replicas = 0
		rootDeployment.Status.UpdatedReplicas = 0
		rootDeployment.Status.ReadyReplicas = 0
		rootDeployment.Status.AvailableReplicas = 0
		rootDeployment.Status.UnavailableReplicas = 0
		for _, o := range others {
			rootDeployment.Status.Replicas += o.Status.Replicas
			rootDeployment.Status.UpdatedReplicas += o.Status.UpdatedReplicas
			rootDeployment.Status.ReadyReplicas += o.Status.ReadyReplicas
			rootDeployment.Status.AvailableReplicas += o.Status.AvailableReplicas
			rootDeployment.Status.UnavailableReplicas += o.Status.UnavailableReplicas
		}

		// Cheat and set the root .status.conditions to the first leaf's .status.conditions.
		// TODO: do better.
		if len(others) > 0 {
			rootDeployment.Status.Conditions = others[0].Status.Conditions
		}

		if _, err := c.client.Deployments(rootDeployment.Namespace).UpdateStatus(ctx, rootDeployment, metav1.UpdateOptions{}); err != nil {
			if errors.IsConflict(err) {
				key, err := cache.MetaNamespaceKeyFunc(deployment)
				if err != nil {
					return err
				}
				c.queue.AddRateLimited(key)
				return nil
			}
			return err
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
	rest := *root.Spec.Replicas % int32(len(cls))
	for index, cl := range cls {
		vd := root.DeepCopy()

		// TODO: munge cluster name
		vd.Name = fmt.Sprintf("%s--%s", root.Name, cl.Name)

		if vd.Labels == nil {
			vd.Labels = map[string]string{}
		}
		vd.Labels[clusterLabel] = cl.Name
		vd.Labels[ownedByLabel] = root.Name

		replicasToSet := replicasEach
		if index == 0 {
			replicasToSet += rest
		}
		vd.Spec.Replicas = &replicasToSet

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
		klog.Infof("created child deployment %q", vd.Name)
	}

	return nil
}
