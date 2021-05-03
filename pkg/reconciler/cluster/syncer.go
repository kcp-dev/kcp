package cluster

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	appsv1 "k8s.io/api/apps/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog"
)

const (
	syncerNS      = "syncer-system"
	syncerSAName  = "syncer"
	syncerPrefix = "syncer"
)

func syncerWorkloadName(logicalCluster string) string {
	return syncerPrefix + "-from-" + logicalCluster
}

func syncerConfigMapName(logicalCluster string) string {
	return "kubeconfig-for-" + logicalCluster
}

// installSyncer installs the syncer image on the target cluster.
//
// It takes the syncer image name to run, and the kubeconfig of the kcp
func installSyncer(ctx context.Context, client kubernetes.Interface, syncerImage, kubeconfig, clusterID, logicalCluster string) error {
	// Create Namespace
	if _, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: syncerNS,
		},
	}, metav1.CreateOptions{}); err != nil && !k8serrors.IsAlreadyExists(err) {
		return err
	}

	// Create ServiceAccount.
	if _, err := client.CoreV1().ServiceAccounts(syncerNS).Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: syncerNS,
			Name:      syncerSAName,
		},
	}, metav1.CreateOptions{}); err != nil && !k8serrors.IsAlreadyExists(err) {
		return err
	}

	// TODO: Create or Update ClusterRole

	// TODO: Create ClusterRoleBinding

	// Populate a ConfigMap with the kubeconfig to reach the kcp, to be
	// mounted into the syncer's Pod.
	configMap := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: syncerNS,
			Name:      syncerConfigMapName(logicalCluster),
		},
		Data: map[string]string{
			"kubeconfig": kubeconfig,
		},
	}
	if _, err := client.CoreV1().ConfigMaps(syncerNS).Create(ctx, &configMap, metav1.CreateOptions{}); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			if _, err := client.CoreV1().ConfigMaps(syncerNS).Update(ctx, &configMap, metav1.UpdateOptions{}); err != nil {
			}
		} else {
			return err
		}
	}

	var one int32 = 1
	// Create or Update Pod
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: syncerNS,
			Name:      syncerWorkloadName(logicalCluster),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &one,
			Selector: &metav1.LabelSelector {
				MatchLabels: map[string]string{
					"app": syncerWorkloadName(logicalCluster),
				},
			},
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RecreateDeploymentStrategyType,
			},
			Template: corev1.PodTemplateSpec {
				ObjectMeta: metav1.ObjectMeta {
					Labels: map[string]string{
						"app": syncerWorkloadName(logicalCluster),
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "syncer",
						Image: syncerImage,
						Args: []string{
							"-cluster", clusterID,
							"-kubeconfig", "/kcp/kubeconfig",
						},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "kubeconfig",
							MountPath: "/kcp",
							ReadOnly:  true,
						}},
						TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
					}},
					Volumes: []corev1.Volume{{
						Name: "kubeconfig",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: syncerConfigMapName(logicalCluster),
								},
								Items: []corev1.KeyToPath{{
									Key: "kubeconfig", Path: "kubeconfig",
								}},
							},
						},
					}},
				},
			},
		},		
	}
	if _, err := client.AppsV1().Deployments(syncerNS).Create(ctx, deployment, metav1.CreateOptions{}); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			// Update Deployment
			if _, err := client.AppsV1().Deployments(syncerNS).Update(ctx, deployment, metav1.UpdateOptions{}); err != nil {
				klog.Error(err)
				return err
			}
		} else {
			return err
		}
	}
	return nil
}

func healthcheckSyncer(ctx context.Context, client kubernetes.Interface, logicalCluster string) error {
	pods, err := client.CoreV1().Pods(syncerNS).List(ctx, metav1.ListOptions{LabelSelector: "app=" + syncerConfigMapName(logicalCluster) })
	if err != nil {
		return err
	}
	if len(pods.Items) == 0 {
		return fmt.Errorf("Syncer pod not ready: not syncer pod found")
	}
	if len(pods.Items) > 1 {
		return fmt.Errorf("Syncer pod not ready: there should be only 1 syncer pod")
	}
	pod := pods.Items[0]
	if pod.Status.Phase != corev1.PodRunning {
		return fmt.Errorf("Syncer pod not ready: %s", pod.Status.Phase)
	}
	return nil
}
