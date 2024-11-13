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

	conditionsapi "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	"github.com/kcp-dev/logicalcluster/v3"
	helmclient "github.com/mittwald/go-helm-client"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	mountsv1alpha1 "github.com/kcp-dev/kcp/contrib/mounts-vw/apis/mounts/v1alpha1"
	"github.com/kcp-dev/kcp/contrib/mounts-vw/state"
)

const (
// proxyPrefix = "/services/cluster-proxy/"
)

// deprovisionerReconciler is a reconciler reconciles mounts deletes vcluster
// in the target cluster.
type deprovisionerReconciler struct {
	getState func(key string) (state.Value, bool)
}

func (r *deprovisionerReconciler) reconcile(ctx context.Context, mount *mountsv1alpha1.VCluster) (reconcileStatus, error) {
	if mount.DeletionTimestamp == nil { // creation is handled by the provisioner.
		return reconcileStatusContinue, nil
	}
	log := klog.FromContext(ctx)

	log.Info("deprovisioning vcluster", "name", mount.Name)
	if mount.Spec.Mode != mountsv1alpha1.KubeClusterModeDelegated {
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
		return reconcileStatusStopAndRequeue, err
	}

	releases, err := helmClient.ListDeployedReleases()
	if err != nil {
		return reconcileStatusStopAndRequeue, err
	}

	var foundToDelete bool
	for _, release := range releases {
		if release.Name == "vcluster" && release.Namespace == name {
			foundToDelete = true
			chartSpec := &helmclient.ChartSpec{
				ReleaseName:     "vcluster",
				ChartName:       "vcluster/vcluster",
				Namespace:       name,
				CreateNamespace: true,
				ValuesYaml:      "",
				Version:         release.Chart.AppVersion(),
			}
			err := helmClient.UninstallRelease(chartSpec)
			if err != nil {
				klog.Warning(err)
				return reconcileStatusStopAndRequeue, err
			}
		}
	}

	if !foundToDelete {
		conditions.Set(mount, &conditionsapi.Condition{
			Type:    mountsv1alpha1.ClusterDeleted,
			Status:  corev1.ConditionTrue,
			Message: fmt.Sprintf("vclusters %s not found. Ready to be deleted.", name),
		})
	}

	return reconcileStatusStopAndRequeue, nil // wait for all to be gone.
}
