/*
Copyright 2024 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package vcluster

import (
	"context"
	"fmt"
	"net/url"
	"path"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	conditionsapi "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mountsv1alpha1 "github.com/kcp-dev/kcp/contrib/mounts-vw/apis/mounts/v1alpha1"
	"github.com/kcp-dev/kcp/contrib/mounts-vw/state"
	"github.com/kcp-dev/kcp/contrib/mounts-vw/utils/clientgo"
	"github.com/kcp-dev/kcp/contrib/mounts-vw/utils/secrets"
)

const (
	proxyPrefix = "/services/cluster-proxy/"
)

// directReconciler is a reconciler reconciles mounts, which are direct and
// has secret set.
type directReconciler struct {
	getState               func(key string) (state.Value, bool)
	setState               func(key string, value state.Value)
	getSecret              func(ctx context.Context, cluster logicalcluster.Path, namespace, name string) (*corev1.Secret, error)
	deleteState            func(key string)
	getVirtualWorkspaceURL func() *url.URL
}

func (r *directReconciler) reconcile(ctx context.Context, mount *mountsv1alpha1.VCluster) (reconcileStatus, error) {
	if mount.Spec.Mode != mountsv1alpha1.KubeClusterModeDirect {
		return reconcileStatusContinue, nil
	}

	secretRef := mount.Spec.SecretRef
	if secretRef == nil {
		conditions.Set(mount, &conditionsapi.Condition{
			Type:    mountsv1alpha1.ClusterReady,
			Status:  corev1.ConditionFalse,
			Message: "secretRef is not set",
		})
		return reconcileStatusStopAndRequeue, nil
	}

	cluster := logicalcluster.NewPath(logicalcluster.From(mount).String())

	mount.Status.Phase = tenancyv1alpha1.MountPhaseConnecting

	// set secret first:
	if mount.Status.SecretString == "" {
		secretString, err := secrets.GenerateSecret(16)
		if err != nil {
			return reconcileStatusStopAndRequeue, err
		}
		mount.Status.SecretString = secretString
	}

	secret, err := r.getSecret(ctx, cluster, secretRef.Namespace, secretRef.Name)
	if err != nil {
		conditions.Set(mount, &conditionsapi.Condition{
			Type:    mountsv1alpha1.ClusterReady,
			Status:  corev1.ConditionFalse,
			Message: fmt.Sprintf("failed to get secret %s/%s: %v", secretRef.Namespace, secretRef.Name, err),
		})
		r.deleteState(mount.Status.SecretString)
		return reconcileStatusStopAndRequeue, err
	}

	if secret.Data["kubeconfig"] == nil {
		conditions.Set(mount, &conditionsapi.Condition{
			Type:    mountsv1alpha1.ClusterReady,
			Status:  corev1.ConditionFalse,
			Message: fmt.Sprintf("secret %s/%s does not contain 'kubeconfig' key", secretRef.Namespace, secretRef.Name),
		})
		r.deleteState(mount.Status.SecretString)
		return reconcileStatusStopAndRequeue, nil
	}

	clientset, rest, err := clientgo.GetClientFromKubeConfig(secret.Data["kubeconfig"])
	if err != nil {
		conditions.Set(mount, &conditionsapi.Condition{
			Type:    mountsv1alpha1.ClusterReady,
			Status:  corev1.ConditionFalse,
			Message: fmt.Sprintf("failed to create client from kubeconfig in secret %s/%s: %v", secretRef.Namespace, secretRef.Name, err),
		})
		r.deleteState(mount.Status.SecretString)
		return reconcileStatusStopAndRequeue, err
	}

	_, err = clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		conditions.Set(mount, &conditionsapi.Condition{
			Type:    mountsv1alpha1.ClusterReady,
			Status:  corev1.ConditionFalse,
			Message: fmt.Sprintf("failed to access namespaces from kubeconfig in secret %s/%s: %v", secretRef.Namespace, secretRef.Name, err),
		})
		r.deleteState(mount.Status.SecretString)
		return reconcileStatusStopAndRequeue, err
	}

	conditions.Set(mount, &conditionsapi.Condition{
		Type:    mountsv1alpha1.ClusterReady,
		Status:  corev1.ConditionTrue,
		Message: fmt.Sprintf("successfully accessed namespaces from kubeconfig in secret %s/%s", secretRef.Namespace, secretRef.Name),
	})

	// construct url for mount:
	kubeClusterVirtualWorkspaceURL := r.getVirtualWorkspaceURL()
	kubeClusterVirtualWorkspaceURL.Path = path.Join(
		proxyPrefix,
		logicalcluster.From(mount).String(),
		"apis",
		mountsv1alpha1.SchemeGroupVersion.String(),
		"kubeclusters",
		mount.Name,
		"proxy",
	)
	mount.Status.URL = kubeClusterVirtualWorkspaceURL.String()

	r.setState(mount.Status.SecretString, state.Value{
		Client: clientset,
		URL:    mount.Status.URL,
		Config: rest,
	})
	mount.Status.Phase = tenancyv1alpha1.MountPhaseReady

	return reconcileStatusContinue, nil
}
