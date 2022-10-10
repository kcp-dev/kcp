/*
Copyright 2021 The KCP Authors.

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

package syncer

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	workloadlisters "github.com/kcp-dev/kcp/pkg/client/listers/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/crdpuller"
	"github.com/kcp-dev/kcp/pkg/logging"
)

var syncTargetKind = reflect.TypeOf(workloadv1alpha1.SyncTarget{}).Name()

func syncTargetAsOwnerReference(obj *workloadv1alpha1.SyncTarget, controller bool) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: workloadv1alpha1.SchemeGroupVersion.String(),
		Kind:       syncTargetKind,
		Name:       obj.Name,
		UID:        obj.UID,
		Controller: &controller,
	}
}

const gvrForSyncTargetInLogicalClusterIndexName = "GVRForSyncTargetInLogicalCluster"

func getGVRForSyncTargetInLogicalClusterIndexKey(syncTarget string, clusterName logicalcluster.Name, gvr metav1.GroupVersionResource) string {
	return syncTarget + "$$" + clusterName.String() + "$" + gvr.String()
}

const syncTargetInLogicalClusterIndexName = "SyncTargetInLogicalCluster"

func getSyncTargetInLogicalClusterIndexKey(syncTarget string, clusterName logicalcluster.Name) string {
	return syncTarget + "/" + clusterName.String()
}

func NewAPIImporter(
	upstreamConfig, downstreamConfig *rest.Config,
	kcpInformerFactory kcpinformers.SharedInformerFactory,
	resourcesToSync []string,
	logicalClusterName logicalcluster.Name,
	syncTargetName string,
) (*APIImporter, error) {
	agent := fmt.Sprintf("kcp-workload-api-importer-%s-%s", logicalClusterName, syncTargetName)
	upstreamConfig = rest.AddUserAgent(rest.CopyConfig(upstreamConfig), agent)
	downstreamConfig = rest.AddUserAgent(rest.CopyConfig(downstreamConfig), agent)

	kcpClusterClient, err := kcpclient.NewClusterForConfig(upstreamConfig)
	if err != nil {
		return nil, err
	}

	importIndexer := kcpInformerFactory.Apiresource().V1alpha1().APIResourceImports().Informer().GetIndexer()

	indexers := map[string]cache.IndexFunc{
		gvrForSyncTargetInLogicalClusterIndexName: func(obj interface{}) ([]string, error) {
			if apiResourceImport, ok := obj.(*apiresourcev1alpha1.APIResourceImport); ok {
				return []string{getGVRForSyncTargetInLogicalClusterIndexKey(apiResourceImport.Spec.Location, logicalcluster.From(apiResourceImport), apiResourceImport.GVR())}, nil
			}
			return []string{}, nil
		},
		syncTargetInLogicalClusterIndexName: func(obj interface{}) ([]string, error) {
			if apiResourceImport, ok := obj.(*apiresourcev1alpha1.APIResourceImport); ok {
				return []string{getSyncTargetInLogicalClusterIndexKey(apiResourceImport.Spec.Location, logicalcluster.From(apiResourceImport))}, nil
			}
			return []string{}, nil
		},
	}

	// Ensure the indexers are only added if not already present.
	for indexName := range importIndexer.GetIndexers() {
		delete(indexers, indexName)
	}
	if len(indexers) > 0 {
		if err := importIndexer.AddIndexers(indexers); err != nil {
			return nil, fmt.Errorf("failed to add indexer for API Importer: %w", err)
		}
	}

	schemaPuller, err := crdpuller.NewSchemaPuller(downstreamConfig)
	if err != nil {
		return nil, err
	}

	return &APIImporter{
		kcpInformerFactory:       kcpInformerFactory,
		kcpClusterClient:         kcpClusterClient,
		resourcesToSync:          resourcesToSync,
		apiresourceImportIndexer: importIndexer,
		syncTargetLister:         kcpInformerFactory.Workload().V1alpha1().SyncTargets().Lister(),

		syncTargetName:     syncTargetName,
		logicalClusterName: logicalClusterName,
		schemaPuller:       schemaPuller,
	}, nil
}

type APIImporter struct {
	kcpInformerFactory       kcpinformers.SharedInformerFactory
	kcpClusterClient         *kcpclient.Cluster
	resourcesToSync          []string
	apiresourceImportIndexer cache.Indexer
	syncTargetLister         workloadlisters.SyncTargetLister

	syncTargetName     string
	logicalClusterName logicalcluster.Name
	schemaPuller       crdpuller.SchemaPuller
	SyncedGVRs         map[string]metav1.GroupVersionResource
}

func (i *APIImporter) Start(ctx context.Context, pollInterval time.Duration) {
	defer runtime.HandleCrash()

	i.kcpInformerFactory.WaitForCacheSync(ctx.Done())
	logger := logging.WithReconciler(klog.FromContext(ctx), "api-importer")
	ctx = klog.NewContext(ctx, logger)

	logger.Info("Starting API Importer")

	clusterContext := request.WithCluster(ctx, request.Cluster{Name: i.logicalClusterName})
	go wait.UntilWithContext(clusterContext, func(innerCtx context.Context) {
		i.ImportAPIs(innerCtx)
	}, pollInterval)

	<-ctx.Done()
	i.Stop(ctx)
}

func (i *APIImporter) Stop(ctx context.Context) {
	logger := klog.FromContext(ctx)
	logger.Info("stopping API Importer")

	objs, err := i.apiresourceImportIndexer.ByIndex(
		syncTargetInLogicalClusterIndexName,
		getSyncTargetInLogicalClusterIndexKey(i.syncTargetName, i.logicalClusterName),
	)
	if err != nil {
		logger.Error(err, "error trying to list APIResourceImport objects")
	}
	for _, obj := range objs {
		apiResourceImportToDelete := obj.(*apiresourcev1alpha1.APIResourceImport)
		logger := logger.WithValues("apiResourceImport", apiResourceImportToDelete.Name)
		err := i.kcpClusterClient.Cluster(i.logicalClusterName).ApiresourceV1alpha1().APIResourceImports().Delete(request.WithCluster(context.Background(), request.Cluster{Name: i.logicalClusterName}), apiResourceImportToDelete.Name, metav1.DeleteOptions{})
		if err != nil {
			logger.Error(err, "error deleting APIResourceImport")
		}
	}
}

func (i *APIImporter) ImportAPIs(ctx context.Context) {
	logger := klog.FromContext(ctx)

	// TODO(skuznets): can we figure out how to not leak this detail up to this code?
	// I guess once the indexer is using kcpcache.MetaClusterNamespaceKeyFunc, we can just use that formatter ...
	var syncTargetKey string
	syncTargetKey += i.logicalClusterName.String() + "|"
	syncTargetKey += i.syncTargetName

	syncTarget, err := i.syncTargetLister.Get(syncTargetKey)

	if err != nil {
		logger.Error(err, "error getting syncTarget")
		return
	}

	// merge resourceToSync from synctarget with resourcesToSync set on the syncer's flag.
	resourceToSyncSet := sets.NewString(i.resourcesToSync...)
	for _, rs := range syncTarget.Status.SyncedResources {
		resourceToSyncSet.Insert(fmt.Sprintf("%s.%s", rs.Resource, rs.Group))
	}
	resourcesToSync := resourceToSyncSet.List()

	logger.V(2).Info("Importing APIs", "resourcesToImport", resourcesToSync)
	crds, err := i.schemaPuller.PullCRDs(ctx, resourcesToSync...)
	if err != nil {
		logger.Error(err, "error pulling CRDs")
		return
	}

	gvrsToSync := map[string]metav1.GroupVersionResource{}
	for groupResource, pulledCrd := range crds {
		crdVersion := pulledCrd.Spec.Versions[0]
		gvr := metav1.GroupVersionResource{
			Group:    pulledCrd.Spec.Group,
			Version:  crdVersion.Name,
			Resource: groupResource.Resource,
		}
		logger := logger.WithValues(
			"group", gvr.Group,
			"version", gvr.Version,
			"resource", gvr.Resource,
		)

		objs, err := i.apiresourceImportIndexer.ByIndex(
			gvrForSyncTargetInLogicalClusterIndexName,
			getGVRForSyncTargetInLogicalClusterIndexKey(i.syncTargetName, i.logicalClusterName, gvr),
		)
		if err != nil {
			logger.Error(err, "error pulling CRDs")
			continue
		}
		if len(objs) > 1 {
			logger.Error(fmt.Errorf("there should be only one APIResourceImport but there was %d", len(objs)), "err importing APIs")
			continue
		}
		if len(objs) == 1 {
			apiResourceImport := objs[0].(*apiresourcev1alpha1.APIResourceImport).DeepCopy()
			if err := apiResourceImport.Spec.SetSchema(crdVersion.Schema.OpenAPIV3Schema); err != nil {
				logger.Error(err, "error setting schema")
				continue
			}
			logger = logger.WithValues("apiResourceImport", apiResourceImport.Name)
			logger.Info("updating APIResourceImport")
			if _, err := i.kcpClusterClient.Cluster(i.logicalClusterName).ApiresourceV1alpha1().APIResourceImports().Update(ctx, apiResourceImport, metav1.UpdateOptions{}); err != nil {
				logger.Error(err, "error updating APIResourceImport")
				continue
			}
		} else {
			apiResourceImportName := gvr.Resource + "." + i.syncTargetName + "." + gvr.Version + "."
			if gvr.Group == "" {
				apiResourceImportName += "core"
			} else {
				apiResourceImportName += gvr.Group
			}
			groupVersion := apiresourcev1alpha1.GroupVersion{
				Group:   gvr.Group,
				Version: gvr.Version,
			}
			apiResourceImport := &apiresourcev1alpha1.APIResourceImport{
				ObjectMeta: metav1.ObjectMeta{
					Name: apiResourceImportName,
					OwnerReferences: []metav1.OwnerReference{
						syncTargetAsOwnerReference(syncTarget, true),
					},
					Annotations: map[string]string{
						logicalcluster.AnnotationKey:             i.logicalClusterName.String(),
						apiresourcev1alpha1.APIVersionAnnotation: groupVersion.APIVersion(),
					},
				},
				Spec: apiresourcev1alpha1.APIResourceImportSpec{
					Location:             i.syncTargetName,
					SchemaUpdateStrategy: apiresourcev1alpha1.UpdateUnpublished,
					CommonAPIResourceSpec: apiresourcev1alpha1.CommonAPIResourceSpec{
						GroupVersion: apiresourcev1alpha1.GroupVersion{
							Group:   gvr.Group,
							Version: gvr.Version,
						},
						Scope:                         pulledCrd.Spec.Scope,
						CustomResourceDefinitionNames: pulledCrd.Spec.Names,
						SubResources:                  *(&apiresourcev1alpha1.SubResources{}).ImportFromCRDVersion(&crdVersion),
						ColumnDefinitions:             *(&apiresourcev1alpha1.ColumnDefinitions{}).ImportFromCRDVersion(&crdVersion),
					},
				},
			}
			if err := apiResourceImport.Spec.SetSchema(crdVersion.Schema.OpenAPIV3Schema); err != nil {
				logger.Error(err, "error setting schema")
				continue
			}
			if value, found := pulledCrd.Annotations[apiextensionsv1.KubeAPIApprovedAnnotation]; found {
				apiResourceImport.Annotations[apiextensionsv1.KubeAPIApprovedAnnotation] = value
			}

			logger.Info("creating APIResourceImport")
			if _, err := i.kcpClusterClient.Cluster(i.logicalClusterName).ApiresourceV1alpha1().APIResourceImports().Create(ctx, apiResourceImport, metav1.CreateOptions{}); err != nil {
				logger.Error(err, "error creating APIResourceImport")
				continue
			}
		}
		gvrsToSync[gvr.String()] = gvr
	}

	gvrsToRemove := sets.StringKeySet(i.SyncedGVRs).Difference(sets.StringKeySet(gvrsToSync))
	for _, gvrToRemove := range gvrsToRemove.UnsortedList() {
		gvr := i.SyncedGVRs[gvrToRemove]
		objs, err := i.apiresourceImportIndexer.ByIndex(
			gvrForSyncTargetInLogicalClusterIndexName,
			getGVRForSyncTargetInLogicalClusterIndexKey(i.syncTargetName, i.logicalClusterName, gvr),
		)
		logger := logger.WithValues(
			"group", gvr.Group,
			"version", gvr.Version,
			"resource", gvr.Resource,
		)

		if err != nil {
			logger.Error(err, "error pulling CRDs")
			continue
		}
		if len(objs) > 1 {
			logger.Error(fmt.Errorf("there should be only one APIResourceImport of GVR but there was %d", len(objs)), "err deleting APIResourceImport")
			continue
		}
		if len(objs) == 1 {
			apiResourceImportToRemove := objs[0].(*apiresourcev1alpha1.APIResourceImport)
			logger = logger.WithValues("apiResourceImport", apiResourceImportToRemove.Name)
			logger.Info("deleting APIResourceImport")
			err := i.kcpClusterClient.Cluster(i.logicalClusterName).ApiresourceV1alpha1().APIResourceImports().Delete(ctx, apiResourceImportToRemove.Name, metav1.DeleteOptions{})
			if err != nil {
				logger.Error(err, "error deleting APIResourceImport")
				continue
			}
		}
	}
}
