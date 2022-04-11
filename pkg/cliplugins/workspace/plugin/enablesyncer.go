/*
Copyright 2022 The KCP Authors.

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

package plugin

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
)

type SyncerConfig struct {
	Token               []byte
	CACert              string
	Server              string
	KCPNamespace        string
	WorkloadClusterName string
	KCPClusterName      logicalcluster.LogicalCluster
	SyncerImage         string
}

// TODO(marun) Make this method idempotent
func EnableSyncer(ctx context.Context, config *rest.Config, workloadClusterName, syncerImage string, resourcesToSync []string) (*SyncerConfig, error) {
	kcpClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kcp client: %w", err)
	}

	workloadCluster, err := kcpClient.WorkloadV1alpha1().WorkloadClusters().Create(ctx,
		&workloadv1alpha1.WorkloadCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: workloadClusterName,
			},
		},
		metav1.CreateOptions{},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create WorkloadCluster: %w", err)
	}

	kubeClient, err := kubernetesclientset.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	// TODO(marun) Maybe support configuring namespaces other than default?
	defaultNamespace := "default"

	syncerServiceAccountName := fmt.Sprintf("syncer-%s", workloadClusterName)
	sa, err := kubeClient.CoreV1().ServiceAccounts(defaultNamespace).Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name: syncerServiceAccountName,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: workloadv1alpha1.SchemeGroupVersion.String(),
				Kind:       reflect.TypeOf(workloadv1alpha1.WorkloadCluster{}).Name(),
				Name:       workloadCluster.Name,
				UID:        workloadCluster.UID,
			}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create ServiceAccount: %w", err)
	}

	// Wait for the service account to be updated with the name of the token secret
	tokenSecretName := ""
	err = wait.PollImmediateWithContext(ctx, 100*time.Millisecond, 20*time.Second, func(ctx context.Context) (bool, error) {
		serviceAccount, err := kubeClient.CoreV1().ServiceAccounts(defaultNamespace).Get(ctx, sa.Name, metav1.GetOptions{})
		if err != nil {
			klog.V(5).Infof("failed to retrieve ServiceAccount: %v", err)
			return false, nil
		}
		if len(serviceAccount.Secrets) == 0 {
			return false, nil
		}
		tokenSecretName = serviceAccount.Secrets[0].Name
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("timed out waiting for token secret name to be set on ServiceAccount %s/%s", defaultNamespace, sa.Name)
	}

	tokenSecret, err := kubeClient.CoreV1().Secrets(defaultNamespace).Get(ctx, tokenSecretName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve Secret: %w", err)
	}
	saToken := tokenSecret.Data["token"]
	if len(saToken) == 0 {
		return nil, fmt.Errorf("token secret %s/%s is missing a value for `token`", defaultNamespace, tokenSecretName)
	}

	caConfigMapName := "kube-root-ca.crt"
	caConfigMap, err := kubeClient.CoreV1().ConfigMaps(defaultNamespace).Get(ctx, caConfigMapName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve ConfigMap: %w", err)
	}
	caCert := caConfigMap.Data["ca.crt"]
	if len(caCert) == 0 {
		return nil, fmt.Errorf("token secret %s/%s is missing a value for `ca.crt`", defaultNamespace, caConfigMapName)

	}

	return &SyncerConfig{
		Server:              config.Host,
		Token:               saToken,
		CACert:              caCert,
		KCPNamespace:        defaultNamespace,
		KCPClusterName:      logicalcluster.From(workloadCluster),
		WorkloadClusterName: workloadCluster.Name,
		SyncerImage:         syncerImage,
	}, nil
}
