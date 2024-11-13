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
	"strings"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	conditionsapi "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	"github.com/kcp-dev/logicalcluster/v3"
	helmclient "github.com/mittwald/go-helm-client"
	"helm.sh/helm/v3/pkg/repo"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	mountsv1alpha1 "github.com/kcp-dev/kcp/contrib/mounts-vw/apis/mounts/v1alpha1"
	"github.com/kcp-dev/kcp/contrib/mounts-vw/state"
	"github.com/kcp-dev/kcp/contrib/mounts-vw/utils/clientgo"
	"github.com/kcp-dev/kcp/contrib/mounts-vw/utils/secrets"
)

const (
// proxyPrefix = "/services/cluster-proxy/"
)

// provisionerReconciler is a reconciler reconciles mounts provisions vcluster
// in the target cluster.
// Responsible for mountsv1alpha1.ClusterReady
type provisionerReconciler struct {
	getState func(key string) (state.Value, bool)
	setState func(key string, value state.Value)
}

func (r *provisionerReconciler) reconcile(ctx context.Context, mount *mountsv1alpha1.VCluster) (reconcileStatus, error) {
	log := klog.FromContext(ctx)
	if mount.DeletionTimestamp != nil || mount.Spec.Mode != mountsv1alpha1.KubeClusterModeDelegated { // deletion is handled by the deprovisioner.
		return reconcileStatusContinue, nil
	}

	v, found := r.getState(*mount.Spec.SecretString)
	if v.Client == nil || !found {
		return reconcileStatusStopAndRequeue, nil
	}

	cluster := logicalcluster.NewPath(logicalcluster.From(mount).String())

	name := cluster.String() + mount.Name

	opt := &helmclient.RestConfClientOptions{
		Options: &helmclient.Options{
			Namespace:        name,
			RepositoryCache:  "/tmp/.helmcache",
			RepositoryConfig: "/tmp/.helmrepo",
			Debug:            true,
			Linting:          true,
		},
		RestConfig: v.Config,
	}

	helmClient, err := helmclient.NewClientFromRestConf(opt)
	if err != nil {
		conditions.Set(mount, &conditionsapi.Condition{
			Type:    mountsv1alpha1.ClusterReady,
			Status:  corev1.ConditionFalse,
			Message: fmt.Errorf("failed to setup helm client: %v", err).Error(),
		})
		return reconcileStatusStopAndRequeue, err
	}

	// Add the repository where the chart is located
	err = helmClient.AddOrUpdateChartRepo(repo.Entry{
		Name: "vcluster",
		URL:  "https://charts.loft.sh",
	})
	if err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			return reconcileStatusStopAndRequeue, err
		}
	}

	releases, err := helmClient.ListDeployedReleases()
	if err != nil {
		return reconcileStatusStopAndRequeue, err
	}

	var foundHelm bool
	for _, release := range releases {
		if release.Name == "vcluster" && release.Namespace == name {
			if release.Chart.AppVersion() == "0.20.2" {
				log.V(2).Info("vcluster already installed desired version")
				foundHelm = true
			}
		}
	}

	if !foundHelm {
		log.V(2).Info("vcluster installing/upgrading")
		// Define the chart specification
		chartSpec := &helmclient.ChartSpec{
			ReleaseName:     "vcluster",
			ChartName:       "vcluster/vcluster",
			Namespace:       name,
			CreateNamespace: true,
			ValuesYaml:      "",
			Version:         mount.Spec.Version,
		}

		// Install the Helm chart
		_, err = helmClient.InstallOrUpgradeChart(ctx, chartSpec, nil)
		if err != nil {
			conditions.Set(mount, &conditionsapi.Condition{
				Type:    mountsv1alpha1.ClusterReady,
				Status:  corev1.ConditionFalse,
				Message: fmt.Errorf("failed to install vcluster: %v", err).Error(),
			})
			return reconcileStatusStopAndRequeue, err
		}
	}

	// get configMap with kubeconfig
	secret, err := v.Client.CoreV1().Secrets(name).Get(ctx, "vc-vcluster", metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return reconcileStatusStopAndRequeue, nil
		}
		return reconcileStatusStopAndRequeue, err
	}

	_, rest, err := clientgo.GetClientFromKubeConfig(secret.Data["config"])
	if err != nil {
		return reconcileStatusStopAndRequeue, err
	}

	v.VClusterConfig = rest
	v.VClusterNamespace = name

	var secretString string
	if mount.Status.SecretString == "" {
		secretString, err = secrets.GenerateSecret(16)
		if err != nil {
			return reconcileStatusStopAndRequeue, err
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

	conditions.Set(mount, &conditionsapi.Condition{
		Type:   mountsv1alpha1.ClusterReady,
		Status: corev1.ConditionTrue,
	})
	mount.Status.Phase = tenancyv1alpha1.MountPhaseReady

	return reconcileStatusContinue, nil
}
