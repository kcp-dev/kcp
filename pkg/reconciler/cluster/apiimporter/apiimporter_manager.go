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

package apiimporter

import (
	"context"
	"time"

	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/pkg/crdpuller"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

type apiImporterManager struct {
	kcpClusterClient         *kcpclient.Cluster
	resourcesToSync          []string
	clusterIndexer           cache.Indexer
	apiresourceImportIndexer cache.Indexer
	apiImporters             map[string]*APIImporter
}

func (m *apiImporterManager) Reconcile(ctx context.Context, cluster *clusterv1alpha1.Cluster) error {
	klog.Infof("reconciling cluster %q", cluster.Name)

	logicalCluster := cluster.GetClusterName()

	// Get client from kubeconfig
	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(cluster.Spec.KubeConfig))
	if err != nil {
		klog.Errorf("invalid kubeconfig: %v", err)
		conditions.MarkFalse(cluster, clusterv1alpha1.ClusterReadyCondition, clusterv1alpha1.InvalidKubeConfigReason, conditionsv1alpha1.ConditionSeverityError, "Error invalid kubeconfig: %v", err.Error())
		return nil // Don't retry.
	}

	if m.apiImporters[cluster.Name] == nil {
		// TODO(sttts): make polling interval configurable for testing. This might flake otherwise.
		apiImporter, err := m.startAPIImporter(cfg, cluster.Name, logicalCluster, time.Minute)
		if err != nil {
			klog.Errorf("error starting the API importer: %v", err)
			conditions.MarkFalse(cluster, clusterv1alpha1.ClusterReadyCondition, clusterv1alpha1.ErrorStartingAPIImporterReason, conditionsv1alpha1.ConditionSeverityError, "Error starting the API importer: %v", err.Error())
			return nil // Don't retry.
		}
		m.apiImporters[cluster.Name] = apiImporter
	}

	return nil
}

func (m *apiImporterManager) Cleanup(ctx context.Context, deletedCluster *clusterv1alpha1.Cluster) {
	klog.Infof("cleanup resources for cluster %q", deletedCluster.Name)

	if apiImporter := m.apiImporters[deletedCluster.Name]; apiImporter != nil {
		apiImporter.Stop()
		delete(m.apiImporters, deletedCluster.Name)
	}
}

func (m *apiImporterManager) startAPIImporter(config *rest.Config, location string, logicalClusterName string, pollInterval time.Duration) (*APIImporter, error) {
	apiImporter := APIImporter{
		kcpClusterClient:         m.kcpClusterClient,
		resourcesToSync:          m.resourcesToSync,
		apiresourceImportIndexer: m.apiresourceImportIndexer,
		clusterIndexer:           m.clusterIndexer,

		location:           location,
		logicalClusterName: logicalClusterName,
		context:            request.WithCluster(context.Background(), request.Cluster{Name: logicalClusterName}),
	}

	ticker := time.NewTicker(pollInterval)
	apiImporter.done = make(chan bool)

	var err error
	apiImporter.schemaPuller, err = crdpuller.NewSchemaPuller(config)
	if err != nil {
		return nil, err
	}

	go func() {
		apiImporter.ImportAPIs()
		for {
			select {
			case <-apiImporter.done:
				return
			case <-ticker.C:
				apiImporter.ImportAPIs()
			}
		}
	}()

	return &apiImporter, nil
}
