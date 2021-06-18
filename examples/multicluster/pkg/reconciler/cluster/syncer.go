package cluster

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog"
)

const (
	syncerNS     = "syncer-system"
	syncerSAName = "syncer"
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
func installSyncer(ctx context.Context, client kubernetes.Interface, syncerImage, kubeconfig, clusterID, logicalCluster string, apiGroups, resourcesToSync []string) error {
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

	// Create or Update ClusterRole

	var resourcesWithStatus []string
	resourcesWithStatus = append(resourcesWithStatus, resourcesToSync...)
	for _, resourceToSync := range resourcesToSync {
		resourcesWithStatus = append(resourcesWithStatus, resourceToSync+"/status")
	}

	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: syncerSAName,
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:     []string{"create"},
				APIGroups: []string{""},
				Resources: []string{"namespaces"},
			},
			{
				Verbs:     []string{"create", "update", "get"},
				Resources: resourcesWithStatus,
				APIGroups: apiGroups,
			},
		},
	}
	if _, err := client.RbacV1().ClusterRoles().Create(ctx, clusterRole, metav1.CreateOptions{}); err != nil {
		if !k8serrors.IsAlreadyExists(err) {
			return err
		}
		existing, err := client.RbacV1().ClusterRoles().Get(ctx, clusterRole.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if !equality.Semantic.DeepEqual(existing.Rules, clusterRole.Rules) {
			clusterRole.ResourceVersion = existing.ResourceVersion
			if _, err := client.RbacV1().ClusterRoles().Update(ctx, clusterRole, metav1.UpdateOptions{}); err != nil {
				return err
			}
		}
	}

	// Create ClusterRoleBinding

	if _, err := client.RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: syncerSAName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      syncerSAName,
				Namespace: syncerNS,
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			Name:     syncerSAName,
			APIGroup: "rbac.authorization.k8s.io",
		},
	}, metav1.CreateOptions{}); err != nil && !k8serrors.IsAlreadyExists(err) {
		return err
	}

	// Populate a ConfigMap with the kubeconfig to reach the kcp, to be
	// mounted into the syncer's Pod.
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: syncerNS,
			Name:      syncerConfigMapName(logicalCluster),
		},
		Data: map[string]string{
			"kubeconfig": kubeconfig,
		},
	}
	if _, err := client.CoreV1().ConfigMaps(syncerNS).Create(ctx, configMap, metav1.CreateOptions{}); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			if configMap, err = client.CoreV1().ConfigMaps(syncerNS).Update(ctx, configMap, metav1.UpdateOptions{}); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	args := []string{
		"-cluster", clusterID,
		"-from_kubeconfig", "/kcp/kubeconfig",
	}
	args = append(args, resourcesToSync...)

	var one int32 = 1
	// Create or Update Deployment
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: syncerNS,
			Name:      syncerWorkloadName(logicalCluster),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &one,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": syncerWorkloadName(logicalCluster),
				},
			},
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RecreateDeploymentStrategyType,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": syncerWorkloadName(logicalCluster),
					},
					Annotations: map[string]string{
						"kubeconfig/version": configMap.ResourceVersion,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "syncer",
						Image: syncerImage,
						Args:  args,
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
					ServiceAccountName: syncerSAName,
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

// uninstallSyncer uninstalls the syncer from the target cluster by deleting the syncer namespace.
func uninstallSyncer(ctx context.Context, client kubernetes.Interface) {
	if err := client.CoreV1().Namespaces().Delete(ctx, syncerNS, metav1.DeleteOptions{}); err != nil {
		klog.Errorf("Deleting namespace %q: %v", syncerNS, err)
	}
}

func healthcheckSyncer(ctx context.Context, client kubernetes.Interface, logicalCluster string) error {
	pods, err := client.CoreV1().Pods(syncerNS).List(ctx, metav1.ListOptions{LabelSelector: "app=" + syncerConfigMapName(logicalCluster)})
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
