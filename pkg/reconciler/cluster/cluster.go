package cluster

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/crdpuller"
	"github.com/kcp-dev/kcp/pkg/syncer"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog"
)

const (
	pollInterval     = time.Minute
	numSyncerThreads = 2
)

func clusterOriginLabel(clusterID string) string {
	return "imported-from/" + clusterID
}

func (c *Controller) reconcile(ctx context.Context, cluster *v1alpha1.Cluster) error {
	log.Println("reconciling cluster", cluster.Name)

	logicalCluster := cluster.GetClusterName()
	logicalClusterContext := genericapirequest.WithCluster(ctx, genericapirequest.Cluster{
		Name: logicalCluster,
	})

	// Get client from kubeconfig
	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(cluster.Spec.KubeConfig))
	if err != nil {
		log.Printf("invalid kubeconfig: %v", err)
		cluster.Status.SetConditionReady(corev1.ConditionFalse,
			"InvalidKubeConfig",
			fmt.Sprintf("Invalid kubeconfig: %v", err))
		return nil // Don't retry.
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Printf("error creating client: %v", err)
		cluster.Status.SetConditionReady(corev1.ConditionFalse,
			"ErrorCreatingClient",
			fmt.Sprintf("Error creating client from kubeconfig: %v", err))
		return nil // Don't retry.
	}

	schemaPuller, err := crdpuller.NewSchemaPuller(cfg)
	if err != nil {
		log.Printf("error creating schemapuller: %v", err)
		cluster.Status.SetConditionReady(corev1.ConditionFalse,
			"ErrorCreatingSchemaPuller",
			fmt.Sprintf("Error creating schema puller client from kubeconfig: %v", err))
		return nil // Don't retry.
	}

	apiGroups := sets.NewString()
	resources := sets.NewString()
	crds, err := schemaPuller.PullCRDs(ctx, c.resourcesToSync...)
	if err != nil {
		log.Printf("error pulling CRDs: %v", err)
		cluster.Status.SetConditionReady(corev1.ConditionFalse,
			"ErrorPullingResourceSchemas",
			fmt.Sprintf("Error pulling API Resource Schemas from cluster %s: %v", cluster.Name, err))
		return nil // Don't retry.
	}

	for resourceName, pulledCrd := range crds {
		pulledCrd.SetClusterName(logicalCluster)
		pulledCrd.Labels[clusterOriginLabel(cluster.Name)] = ""
		clusterCrd, err := c.crdClient.CustomResourceDefinitions().Create(logicalClusterContext, pulledCrd, v1.CreateOptions{})
		if errors.IsAlreadyExists(err) {
			clusterCrd, err = c.crdClient.CustomResourceDefinitions().Get(logicalClusterContext, pulledCrd.Name, v1.GetOptions{})
			if err == nil {
				if !equality.Semantic.DeepEqual(pulledCrd.Spec, clusterCrd.Spec) ||
					!equality.Semantic.DeepEqual(pulledCrd.Annotations, clusterCrd.Annotations) ||
					!equality.Semantic.DeepEqual(pulledCrd.Labels, clusterCrd.Labels) {
					pulledCrd.ResourceVersion = clusterCrd.ResourceVersion
					_, err = c.crdClient.CustomResourceDefinitions().Update(logicalClusterContext, pulledCrd, v1.UpdateOptions{})
					if errors.IsConflict(err) {
						log.Printf("Conflict when applying CRD pulled from cluster %s for resource %s: %v ... Retrying\n", cluster.Name, resourceName, err)
						c.enqueue(cluster)
						return nil
					}
				}
			}
		}
		if err != nil {
			log.Printf("error applying pull CRDs: %v", err)
			cluster.Status.SetConditionReady(corev1.ConditionFalse,
				"ErrorApplyingPulledResourceSchemas",
				fmt.Sprintf("Error applying the pulled API Resource Schemas from cluster %s: %v", cluster.Name, err))
			return nil // Don't retry.
		}
		apiGroups.Insert(pulledCrd.Spec.Group)
		resources.Insert(pulledCrd.Spec.Names.Plural)
	}

	if !cluster.Status.Conditions.HasReady() ||
		(c.syncerMode == SyncerModePush && c.syncers[cluster.Name] == nil) {
		kubeConfig := c.kubeconfig.DeepCopy()
		if _, exists := kubeConfig.Contexts[logicalCluster]; !exists {
			log.Printf("error installing syncer: no context with the name of the expected cluster: %s", logicalCluster)
			cluster.Status.SetConditionReady(corev1.ConditionFalse,
				"ErrorInstallingSyncer",
				fmt.Sprintf("Error installing syncer: no context with the name of the expected cluster: %s", logicalCluster))
			return nil // Don't retry.
		}

		switch c.syncerMode {
		case SyncerModePull:
			kubeConfig.CurrentContext = logicalCluster
			bytes, err := clientcmd.Write(*kubeConfig)
			if err != nil {
				log.Printf("error writing kubeconfig for syncer: %v", err)
				cluster.Status.SetConditionReady(corev1.ConditionFalse,
					"ErrorInstallingSyncer",
					fmt.Sprintf("Error installing syncer: %v", err))
				return nil // Don't retry.
			}
			if err := installSyncer(ctx, client, c.syncerImage, string(bytes), cluster.Name, logicalCluster, apiGroups.List(), resources.List()); err != nil {
				log.Printf("error installing syncer: %v", err)
				cluster.Status.SetConditionReady(corev1.ConditionFalse,
					"ErrorInstallingSyncer",
					fmt.Sprintf("Error installing syncer: %v", err))
				return nil // Don't retry.
			}

			log.Println("syncer installing...")
			cluster.Status.SetConditionReady(corev1.ConditionUnknown,
				"SyncerInstalling",
				"Installing syncer on cluster")
		case SyncerModePush:
			kubeConfig.CurrentContext = logicalCluster
			upstream, err := clientcmd.NewNonInteractiveClientConfig(*kubeConfig, logicalCluster, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
			if err != nil {
				log.Printf("error getting kcp kubeconfig: %v", err)
				cluster.Status.SetConditionReady(corev1.ConditionFalse,
					"ErrorStartingSyncer",
					fmt.Sprintf("Error starting syncer: %v", err))
				return nil // Don't retry.
			}

			downstream, err := clientcmd.RESTConfigFromKubeConfig([]byte(cluster.Spec.KubeConfig))
			if err != nil {
				log.Printf("error getting cluster kubeconfig: %v", err)
				cluster.Status.SetConditionReady(corev1.ConditionFalse,
					"ErrorStartingSyncer",
					fmt.Sprintf("Error starting syncer: %v", err))
				return nil // Don't retry.
			}

			specSyncer, err := syncer.NewSpecSyncer(upstream, downstream, resources.List(), cluster.Name)
			if err != nil {
				log.Printf("error starting syncer in push mode: %v", err)
				cluster.Status.SetConditionReady(corev1.ConditionFalse,
					"ErrorStartingSyncer",
					fmt.Sprintf("Error starting syncer: %v", err))
				return err
			}
			statusSyncer, err := syncer.NewStatusSyncer(downstream, upstream, resources.List(), cluster.Name)
			if err != nil {
				specSyncer.Stop()
				log.Printf("error starting syncer in push mode: %v", err)
				cluster.Status.SetConditionReady(corev1.ConditionFalse,
					"ErrorStartingSyncer",
					fmt.Sprintf("Error starting syncer: %v", err))
				return err
			}
			c.syncers[cluster.Name] = &Syncer{
				specSyncer:   specSyncer,
				statusSyncer: statusSyncer,
			}
			specSyncer.Start(numSyncerThreads)
			statusSyncer.Start(numSyncerThreads)
			log.Println("syncer ready!")
			cluster.Status.SetConditionReady(corev1.ConditionTrue,
				"SyncerReady",
				"Syncer ready")
		case SyncerModeNone:
			log.Println("syncer ready!")
			cluster.Status.SetConditionReady(corev1.ConditionTrue,
				"SyncerReady",
				"Syncer ready")
		}
	} else {
		if c.syncerMode == SyncerModePull {
			if err := healthcheckSyncer(ctx, client, logicalCluster); err != nil {
				log.Println("syncer not yet ready")
				cluster.Status.SetConditionReady(corev1.ConditionFalse,
					"SyncerNotReady",
					err.Error())
			} else {
				log.Println("syncer ready!")
				cluster.Status.SetConditionReady(corev1.ConditionTrue,
					"SyncerReady",
					"Syncer ready")
			}
		} else {
			log.Println("syncer ready!")
			cluster.Status.SetConditionReady(corev1.ConditionTrue,
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

func (c *Controller) cleanup(ctx context.Context, deletedCluster *v1alpha1.Cluster) {
	log.Println("cleanup resources for cluster", deletedCluster.Name)

	logicalCluster := deletedCluster.GetClusterName()

	logicalClusterContext := genericapirequest.WithCluster(ctx, genericapirequest.Cluster{
		Name: logicalCluster,
	})

	crds, err := c.crdClient.CustomResourceDefinitions().List(logicalClusterContext, v1.ListOptions{
		LabelSelector: clusterOriginLabel(deletedCluster.Name),
	})
	if err != nil {
		klog.Error(err)
	}
	for _, crd := range crds.Items {
		if len(crd.Labels) == 1 {
			if _, exists := crd.Labels[clusterOriginLabel(deletedCluster.Name)]; exists {
				err := c.crdClient.CustomResourceDefinitions().Delete(logicalClusterContext, crd.Name, v1.DeleteOptions{})
				if err != nil {
					klog.Error(err)
				}
			}
		} else {
			updated := crd.DeepCopy()
			delete(updated.Labels, clusterOriginLabel(deletedCluster.Name))
			_, err := c.crdClient.CustomResourceDefinitions().Update(logicalClusterContext, updated, v1.UpdateOptions{})
			if err != nil {
				klog.Error(err)
			}
		}
	}

	switch c.syncerMode {
	case SyncerModePull:
		// Get client from kubeconfig
		cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(deletedCluster.Spec.KubeConfig))
		if err != nil {
			klog.Errorf("invalid kubeconfig: %v", err)
			return
		}
		client, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			klog.Errorf("error creating client: %v", err)
			return
		}

		uninstallSyncer(ctx, client)
	case SyncerModePush:
		s, ok := c.syncers[deletedCluster.Name]
		if !ok {
			klog.Errorf("could not find syncer for cluster %q", deletedCluster.Name)
			return
		}
		klog.Infof("stopping syncer for cluster %q", deletedCluster.Name)
		s.Stop()
		delete(c.syncers, deletedCluster.Name)
	}
}
