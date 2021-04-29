package cluster

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	syncerNS      = "syncer-system"
	syncerSAName  = "syncer"
	syncerPodName = "syncer"
)

// installSyncer installs the syncer image on the target cluster.
//
// It takes the syncer image name to run, and the kubeconfig of the kcp
func installSyncer(ctx context.Context, client kubernetes.Interface, syncerImage, kubeconfig, clusterID string) error {
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
	if _, err := client.CoreV1().ConfigMaps(syncerNS).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: syncerNS,
			Name:      "kubeconfig",
		},
		Data: map[string]string{
			"kubeconfig": kubeconfig,
		},
	}, metav1.CreateOptions{}); err != nil && !k8serrors.IsAlreadyExists(err) {
		return err
	}

	// Create or Update Pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: syncerNS,
			Name:      syncerPodName,
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
			}},
			Volumes: []corev1.Volume{{
				Name: "kubeconfig",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: "kubeconfig",
						},
						Items: []corev1.KeyToPath{{
							Key: "kubeconfig", Path: "kubeconfig",
						}},
					},
				},
			}},
		},
	}
	if _, err := client.CoreV1().Pods(syncerNS).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			// Update Pod
			if _, err := client.CoreV1().Pods(syncerNS).Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
				return err
			}
		} else {
			return err
		}
	}
	return nil
}

func healthcheckSyncer(ctx context.Context, client kubernetes.Interface) error {
	pod, err := client.CoreV1().Pods(syncerNS).Get(ctx, syncerPodName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if pod.Status.Phase == corev1.PodRunning {
		return nil
	}
	return fmt.Errorf("Syncer pod not ready: %s", pod.Status.Phase)
}
