/*
Copyright 2022 The Kube Bind Authors.

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

package resources

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

func GenerateKubeconfig(ctx context.Context,
	client kubernetes.Interface,
	clusterConfig *rest.Config,
	externalAddress string,
	externalCA []byte,
	externalTLSServerName string,
	saSecretName, ns, kubeconfigSecretName string,
) (*corev1.Secret, error) {
	logger := klog.FromContext(ctx)

	if externalAddress == "" {
		externalAddress = clusterConfig.Host
	}
	if externalCA == nil {
		externalCA = clusterConfig.CAData
	}

	var saSecret *corev1.Secret
	logger.V(2).Info("Waiting for service account secret to be updated with a token", "name", saSecretName)
	if err := wait.PollImmediateWithContext(ctx, 500*time.Millisecond, 10*time.Second, func(ctx context.Context) (done bool, err error) {
		saSecret, err = client.CoreV1().Secrets(ns).Get(ctx, saSecretName, v1.GetOptions{})
		if err != nil && !errors.IsNotFound(err) {
			return false, err
		} else if errors.IsNotFound(err) {
			return false, nil
		}
		return saSecret.Data["token"] != nil && saSecret.Data["ca.crt"] != nil, nil
	}); err != nil {
		return nil, err
	}

	cfg := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"default": {
				Server:                   externalAddress,
				TLSServerName:            externalTLSServerName,
				CertificateAuthorityData: externalCA,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"default": {
				Cluster:   "default",
				Namespace: ns,
				AuthInfo:  "default",
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"default": {
				Token: string(saSecret.Data["token"]),
			},
		},
		CurrentContext: "default",
	}

	kubeconfig, err := clientcmd.Write(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to encode kubeconfig: %w", err)
	}

	kubeconfigSecret := &corev1.Secret{
		ObjectMeta: v1.ObjectMeta{
			Name:      kubeconfigSecretName,
			Namespace: ns,
		},
		Data: map[string][]byte{
			"kubeconfig": kubeconfig,
		},
	}

	logger.V(1).Info("Creating kubeconfig secret", "name", kubeconfigSecretName)
	if secret, err := client.CoreV1().Secrets(ns).Create(ctx, kubeconfigSecret, v1.CreateOptions{}); err != nil && !errors.IsAlreadyExists(err) {
		return nil, err
	} else if err == nil {
		return secret, nil
	}

	var updated *corev1.Secret
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		existing, err := client.CoreV1().Secrets(ns).Get(ctx, kubeconfigSecret.Name, v1.GetOptions{})
		if err != nil {
			return err
		}
		existing.Data = kubeconfigSecret.Data
		logger.V(1).Info("Updating kubeconfig secret", "name", kubeconfigSecretName)
		updated, err = client.CoreV1().Secrets(ns).Update(ctx, existing, v1.UpdateOptions{})
		return err
	}); err != nil {
		return nil, err
	}
	return updated, nil
}
