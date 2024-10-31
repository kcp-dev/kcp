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
	"net/url"
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"
	helmclient "github.com/mittwald/go-helm-client"
	"helm.sh/helm/v3/pkg/repo"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mountsv1alpha1 "github.com/kcp-dev/kcp/contrib/mounts-vw/apis/mounts/v1alpha1"
	"github.com/kcp-dev/kcp/contrib/mounts-vw/state"
	"github.com/kcp-dev/kcp/contrib/mounts-vw/utils/clientgo"
	"github.com/kcp-dev/kcp/contrib/mounts-vw/utils/secrets"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
)

const (
// proxyPrefix = "/services/cluster-proxy/"
)

// provisionerReconciler is a reconciler reconciles mounts provisions vcluster
// in the target cluster.
type provisionerReconciler struct {
	getState func(key string) (state.Value, bool)
	setState func(key string, value state.Value)
}

func (r *provisionerReconciler) reconcile(ctx context.Context, mount *mountsv1alpha1.VCluster) (reconcileStatus, error) {
	if mount.Spec.Mode != mountsv1alpha1.KubeClusterModeDelegated {
		return reconcileStatusContinue, nil
	}

	v, found := r.getState(*mount.Spec.SecretString)
	if v.Client == nil || !found {
		return reconcileAfterRequeue, nil
	}

	cluster := logicalcluster.NewPath(logicalcluster.From(mount).String())

	name := cluster.String() + mount.Name

	opt := &helmclient.RestConfClientOptions{
		Options: &helmclient.Options{
			Namespace:        name,
			RepositoryCache:  "/tmp/.helmcache",
			RepositoryConfig: "/tmp/.helmrepo",
			Debug:            true,
			Linting:          true, // Change this to false if you don't want linting.
			////DebugLog: func(format string, v ...interface{}) {
			//	// Change this to your own logger. Default is 'log.Printf(format, v...)'.
			//},
		},
		RestConfig: v.Config,
	}

	helmClient, err := helmclient.NewClientFromRestConf(opt)
	if err != nil {
		return reconcileAfterRequeue, err
	}

	// Add the repository where the chart is located
	err = helmClient.AddOrUpdateChartRepo(repo.Entry{
		Name: "vcluster",
		URL:  "https://charts.loft.sh",
	})
	if err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			return reconcileAfterRequeue, err
		}
	}

	// Define the chart specification
	chartSpec := &helmclient.ChartSpec{
		ReleaseName:     "vcluster",          // Name of the Helm release
		ChartName:       "vcluster/vcluster", // Chart reference, e.g., "stable/my-chart"
		Namespace:       name,                // Namespace where chart will be installed
		CreateNamespace: true,                // Optional: Create the namespace if it doesn't exist
		ValuesYaml:      "",                  // Optional: YAML configuration for chart values
	}

	// Install the Helm chart
	_, err = helmClient.InstallOrUpgradeChart(ctx, chartSpec, nil)
	if err != nil {
		return reconcileAfterRequeue, err
	}

	// get configMap with kubeconfig
	secret, err := v.Client.CoreV1().Secrets(name).Get(ctx, "vc-vcluster", metav1.GetOptions{})
	if err != nil {
		return reconcileAfterRequeue, err
	}

	_, rest, err := clientgo.GetClientFromKubeConfig(secret.Data["config"])
	if err != nil {
		return reconcileAfterRequeue, err
	}

	v.VClusterConfig = rest
	v.VClusterNamespace = name

	var secretString string
	if mount.Status.SecretString == "" {
		secretString, err = secrets.GenerateSecret(16)
		if err != nil {
			return reconcileAfterRequeue, err
		}
		mount.Status.SecretString = secretString
		r.setState(secretString, v)
	} else {
		secretString = mount.Status.SecretString
	}

	r.setState(secretString, v)

	// secrets string will be purged at VW side.
	// TOOD: This is a temporary solution. We need to find a better way to handle this.
	full, err := url.JoinPath(v.URL, "secret", secretString)
	if err != nil {
		return reconcileStatusStopAndRequeue, err
	}
	mount.Status.URL = full

	mount.Status.Phase = tenancyv1alpha1.MountPhaseReady

	return reconcileStatusContinue, nil
}
