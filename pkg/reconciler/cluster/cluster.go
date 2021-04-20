package cluster

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/crdpuller"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const pollInterval = time.Minute

func (c *Controller) reconcile(ctx context.Context, cluster *v1alpha1.Cluster) error {
	log.Println("reconciling cluster", cluster.Name)

	// Get client from kubeconfig
	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(cluster.Spec.KubeConfig))
	if err != nil {
		cluster.Status.Conditions.SetReady(corev1.ConditionFalse,
			"InvalidKubeConfig",
			fmt.Sprintf("Invalid kubeconfig: %v", err))
		return nil // Don't retry.
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		cluster.Status.Conditions.SetReady(corev1.ConditionFalse,
			"ErrorCreatingClient",
			fmt.Sprintf("Error creating client from kubeconfig: %v", err))
		return nil // Don't retry.
	}

	schemaPuller, err := crdpuller.NewSchemaPuller(cfg)
	if err != nil {
		cluster.Status.Conditions.SetReady(corev1.ConditionFalse,
			"ErrorCreatingClient",
			fmt.Sprintf("Error creating schema puller client from kubeconfig: %v", err))
		return nil // Don't retry.
	}

	crds, err := schemaPuller.PullCRDs(ctx, "pods", "deployments")
	if err != nil {
		cluster.Status.Conditions.SetReady(corev1.ConditionFalse,
			"ErrorPullingResourceSchemas",
			fmt.Sprintf("Error pulling API Resource Schemas from cluster %s: %v", cluster.Name, err))
		return nil // Don't retry.
	}

	for resourceName, pulledCrd := range crds {
		clusterCrd, err := c.crdClient.CustomResourceDefinitions().Create(ctx, pulledCrd, v1.CreateOptions{})
		if errors.IsAlreadyExists(err) {
			clusterCrd, err = c.crdClient.CustomResourceDefinitions().Get(ctx, pulledCrd.Name, v1.GetOptions{})
			if err == nil {
				if !equality.Semantic.DeepEqual(pulledCrd.Spec, clusterCrd.Spec) ||
					!equality.Semantic.DeepEqual(pulledCrd.Annotations, clusterCrd.Annotations) ||
					!equality.Semantic.DeepEqual(pulledCrd.Labels, clusterCrd.Labels) {
					pulledCrd.ResourceVersion = clusterCrd.ResourceVersion
					_, err = c.crdClient.CustomResourceDefinitions().Update(ctx, pulledCrd, v1.UpdateOptions{})
				}
			}
		}
		if err != nil {
			log.Printf("Error when applying CRD pulled from cluster %s for resource %s: %v\n", cluster.Name, resourceName, err)
		}
	}

	if !cluster.Status.Conditions.HasReady() {
		if err := installSyncer(ctx, client, c.syncerImage); err != nil {
			cluster.Status.Conditions.SetReady(corev1.ConditionFalse,
				"ErrorInstallingSyncer",
				fmt.Sprintf("Error installing syncer: %v", err))
			return nil // Don't retry.
		}

		cluster.Status.Conditions.SetReady(corev1.ConditionUnknown,
			"SyncerInstalling",
			"Installing syncer on cluster")
	} else {
		if err := healthcheckSyncer(ctx, client); err != nil {
			cluster.Status.Conditions.SetReady(corev1.ConditionFalse,
				"SyncerNotReady",
				err.Error())
		} else {
			cluster.Status.Conditions.SetReady(corev1.ConditionTrue,
				"SyncerReady",
				"Syncer ready")
		}
	}

	// Enqueue another check later
	c.queue.AddAfter(cluster, pollInterval)
	return nil
}
