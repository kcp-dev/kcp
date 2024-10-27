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

package targets

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

	targetsv1alpha1 "github.com/kcp-dev/kcp/contrib/mounts-vw/apis/targets/v1alpha1"
	"github.com/kcp-dev/kcp/contrib/mounts-vw/state"
)

type targetSecretReconciler struct {
	getSecret              func(ctx context.Context, cluster logicalcluster.Path, namespace, name string) (*corev1.Secret, error)
	setState               func(key string, value state.Value)
	deleteState            func(key string)
	getVirtualWorkspaceURL func() *url.URL
}

const (
	proxyPrefix = "/services/cluster-proxy/"
)

func (r *targetSecretReconciler) reconcile(ctx context.Context, target *targetsv1alpha1.TargetKubeCluster) (reconcileStatus, error) {
	secretRef := target.Spec.SecretRef
	cluster := logicalcluster.NewPath(logicalcluster.From(target).String())

	target.Status.Phase = tenancyv1alpha1.MountPhaseConnecting

	// set secret first:
	if target.Status.SecretString == "" {
		secretString, err := generateSecret(16)
		if err != nil {
			return reconcileStatusStopAndRequeue, err
		}
		target.Status.SecretString = secretString
	}

	secret, err := r.getSecret(ctx, cluster, secretRef.Namespace, secretRef.Name)
	if err != nil {
		conditions.Set(target, &conditionsapi.Condition{
			Type:    targetsv1alpha1.TargetKubeClusterSecretReady,
			Status:  corev1.ConditionFalse,
			Message: fmt.Sprintf("failed to get secret %s/%s: %v", secretRef.Namespace, secretRef.Name, err),
		})
		r.deleteState(target.Status.SecretString)
		return reconcileStatusStopAndRequeue, err
	}

	if secret.Data["kubeconfig"] == nil {
		conditions.Set(target, &conditionsapi.Condition{
			Type:    targetsv1alpha1.TargetKubeClusterSecretReady,
			Status:  corev1.ConditionFalse,
			Message: fmt.Sprintf("secret %s/%s does not contain 'kubeconfig' key", secretRef.Namespace, secretRef.Name),
		})
		r.deleteState(target.Status.SecretString)
		return reconcileStatusStopAndRequeue, nil
	}

	clientset, rest, err := getClientFromKubeConfig(secret.Data["kubeconfig"])
	if err != nil {
		conditions.Set(target, &conditionsapi.Condition{
			Type:    targetsv1alpha1.TargetKubeClusterSecretReady,
			Status:  corev1.ConditionFalse,
			Message: fmt.Sprintf("failed to create client from kubeconfig in secret %s/%s: %v", secretRef.Namespace, secretRef.Name, err),
		})
		r.deleteState(target.Status.SecretString)
		return reconcileStatusStopAndRequeue, err
	}

	_, err = clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		conditions.Set(target, &conditionsapi.Condition{
			Type:    targetsv1alpha1.TargetKubeClusterSecretReady,
			Status:  corev1.ConditionFalse,
			Message: fmt.Sprintf("failed to access namespaces from kubeconfig in secret %s/%s: %v", secretRef.Namespace, secretRef.Name, err),
		})
		r.deleteState(target.Status.SecretString)
		return reconcileStatusStopAndRequeue, err
	}

	conditions.Set(target, &conditionsapi.Condition{
		Type:    targetsv1alpha1.TargetKubeClusterSecretReady,
		Status:  corev1.ConditionTrue,
		Message: fmt.Sprintf("successfully accessed namespaces from kubeconfig in secret %s/%s", secretRef.Namespace, secretRef.Name),
	})

	// construct url for mount:
	kubeClusterVirtualWorkspaceURL := r.getVirtualWorkspaceURL()
	kubeClusterVirtualWorkspaceURL.Path = path.Join(
		proxyPrefix,
		logicalcluster.From(target).String(),
		"apis",
		targetsv1alpha1.SchemeGroupVersion.String(),
		"kubeclusters",
		target.Name,
		"proxy",
	)
	target.Status.URL = kubeClusterVirtualWorkspaceURL.String()

	r.setState(target.Status.SecretString, state.Value{
		Client: clientset,
		URL:    target.Status.URL,
		Config: rest,
	})
	target.Status.Phase = tenancyv1alpha1.MountPhaseReady

	return reconcileStatusContinue, nil
}
