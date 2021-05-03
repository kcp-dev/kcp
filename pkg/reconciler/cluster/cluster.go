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
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog"
)

const pollInterval = time.Minute

func (c *Controller) reconcile(ctx context.Context, cluster *v1alpha1.Cluster) error {
	log.Println("reconciling cluster", cluster.Name)

	// Get client from kubeconfig
	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(cluster.Spec.KubeConfig))
	if err != nil {
		log.Printf("invalid kubeconfig: %v", err)
		cluster.Status.Conditions.SetReady(corev1.ConditionFalse,
			"InvalidKubeConfig",
			fmt.Sprintf("Invalid kubeconfig: %v", err))
		return nil // Don't retry.
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Printf("error creating client: %v", err)
		cluster.Status.Conditions.SetReady(corev1.ConditionFalse,
			"ErrorCreatingClient",
			fmt.Sprintf("Error creating client from kubeconfig: %v", err))
		return nil // Don't retry.
	}

	schemaPuller, err := crdpuller.NewSchemaPuller(cfg)
	if err != nil {
		log.Printf("error creating schemapuller: %v", err)
		cluster.Status.Conditions.SetReady(corev1.ConditionFalse,
			"ErrorCreatingSchemaPuller",
			fmt.Sprintf("Error creating schema puller client from kubeconfig: %v", err))
		return nil // Don't retry.
	}

	crds, err := schemaPuller.PullCRDs(ctx, "pods", "deployments")
	if err != nil {
		log.Printf("error pulling CRDs: %v", err)
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
		logicalCluster := cluster.GetClusterName()
		kubeConfig := c.kubeconfig.DeepCopy()
		if _, exists := kubeConfig.Contexts[logicalCluster]; !exists {
			log.Printf("error installing syncer: no context with the name of the expected cluster: %s", logicalCluster)
			cluster.Status.Conditions.SetReady(corev1.ConditionFalse,
				"ErrorInstallingSyncer",
				fmt.Sprintf("Error installing syncer: no context with the name of the expected cluster: %s", logicalCluster))
			return nil // Don't retry.
		}

		kubeConfig.CurrentContext = logicalCluster
		bytes, err := clientcmd.Write(*kubeConfig)
		if err == nil {
			err = installSyncer(ctx, client, c.syncerImage, string(bytes), cluster.Name)
		}
		if err != nil {
			log.Printf("error installing syncer: %v", err)
			cluster.Status.Conditions.SetReady(corev1.ConditionFalse,
				"ErrorInstallingSyncer",
				fmt.Sprintf("Error installing syncer: %v", err))
			return nil // Don't retry.
		}

		log.Println("syncer installing...")
		cluster.Status.Conditions.SetReady(corev1.ConditionUnknown,
			"SyncerInstalling",
			"Installing syncer on cluster")
	} else {
		if err := healthcheckSyncer(ctx, client); err != nil {
			log.Println("syncer not yet ready")
			cluster.Status.Conditions.SetReady(corev1.ConditionFalse,
				"SyncerNotReady",
				err.Error())
		} else {
			log.Println("syncer ready!")
			cluster.Status.Conditions.SetReady(corev1.ConditionTrue,
				"SyncerReady",
				"Syncer ready")
		}
	}

	// Enqueue another check later
	key, err := cache.MetaNamespaceKeyFunc(cluster)
	if err != nil {
		klog.Error(err)
	} else {
		c.queue.AddAfter(key, pollInterval)
	}
	return nil
}
