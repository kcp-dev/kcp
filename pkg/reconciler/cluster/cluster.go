package cluster

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const pollInterval = time.Minute

func (c *Controller) reconcile(ctx context.Context, cluster *v1alpha1.Cluster) error {
	log.Println("reconciling cluster", cluster.Name)

	// Get client from kubeconfig
	cfg, err := clientcmd.RESTConfigFromKubeConfig(cluster.Spec.KubeConfig)
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

	if cluster.Status.Conditions.HasReady() {
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
