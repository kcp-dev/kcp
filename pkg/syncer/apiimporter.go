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
	"github.com/kcp-dev/kcp/pkg/crdpuller"
	clusterctl "github.com/kcp-dev/kcp/pkg/reconciler/workload/basecontroller"
)

var clusterKind = reflect.TypeOf(workloadv1alpha1.SyncTarget{}).Name()

func clusterAsOwnerReference(obj *workloadv1alpha1.SyncTarget, controller bool) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: workloadv1alpha1.SchemeGroupVersion.String(),
		Kind:       clusterKind,
		Name:       obj.Name,
		UID:        obj.UID,
		Controller: &controller,
	}
}

func NewAPIImporter(
	upstreamConfig, downstreamConfig *rest.Config,
	resourcesToSync []string,
	logicalClusterName logicalcluster.Name,
	location string,
) (*APIImporter, error) {
	agent := fmt.Sprintf("kcp-workload-api-importer-%s-%s", logicalClusterName, location)
	upstreamConfig = rest.AddUserAgent(rest.CopyConfig(upstreamConfig), agent)
	downstreamConfig = rest.AddUserAgent(rest.CopyConfig(downstreamConfig), agent)

	kcpClusterClient, err := kcpclient.NewClusterForConfig(upstreamConfig)
	if err != nil {
		return nil, err
	}

	kcpClient := kcpClusterClient.Cluster(logicalClusterName)
	kcpInformerFactory := kcpinformers.NewSharedInformerFactoryWithOptions(kcpClient, resyncPeriod)
	clusterIndexer := kcpInformerFactory.Workload().V1alpha1().SyncTargets().Informer().GetIndexer()
	importIndexer := kcpInformerFactory.Apiresource().V1alpha1().APIResourceImports().Informer().GetIndexer()

	indexers := map[string]cache.IndexFunc{
		clusterctl.GVRForLocationInLogicalClusterIndexName: func(obj interface{}) ([]string, error) {
			if apiResourceImport, ok := obj.(*apiresourcev1alpha1.APIResourceImport); ok {
				return []string{clusterctl.GetGVRForLocationInLogicalClusterIndexKey(apiResourceImport.Spec.Location, logicalcluster.From(apiResourceImport), apiResourceImport.GVR())}, nil
			}
			return []string{}, nil
		},
		clusterctl.LocationInLogicalClusterIndexName: func(obj interface{}) ([]string, error) {
			if apiResourceImport, ok := obj.(*apiresourcev1alpha1.APIResourceImport); ok {
				return []string{clusterctl.GetLocationInLogicalClusterIndexKey(apiResourceImport.Spec.Location, logicalcluster.From(apiResourceImport))}, nil
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
		clusterIndexer:           clusterIndexer,

		location:           location,
		logicalClusterName: logicalClusterName,
		schemaPuller:       schemaPuller,
	}, nil
}

type APIImporter struct {
	kcpInformerFactory       kcpinformers.SharedInformerFactory
	kcpClusterClient         *kcpclient.Cluster
	resourcesToSync          []string
	apiresourceImportIndexer cache.Indexer
	clusterIndexer           cache.Indexer

	location           string
	logicalClusterName logicalcluster.Name
	schemaPuller       crdpuller.SchemaPuller
	SyncedGVRs         map[string]metav1.GroupVersionResource
}

func (i *APIImporter) Start(ctx context.Context, pollInterval time.Duration) {
	defer runtime.HandleCrash()

	i.kcpInformerFactory.Start(ctx.Done())
	i.kcpInformerFactory.WaitForCacheSync(ctx.Done())

	klog.Infof("Starting API Importer for location %s in cluster %s", i.location, i.logicalClusterName)

	clusterContext := request.WithCluster(ctx, request.Cluster{Name: i.logicalClusterName})
	go wait.UntilWithContext(clusterContext, func(innerCtx context.Context) {
		i.ImportAPIs(innerCtx)
	}, pollInterval)

	<-ctx.Done()
	i.Stop()
}

func (i *APIImporter) Stop() {
	klog.Infof("Stopping API Importer for location %s in cluster %s", i.location, i.logicalClusterName)

	objs, err := i.apiresourceImportIndexer.ByIndex(
		clusterctl.LocationInLogicalClusterIndexName,
		clusterctl.GetLocationInLogicalClusterIndexKey(i.location, i.logicalClusterName),
	)
	if err != nil {
		klog.Errorf("error trying to list APIResourceImport objects for location %s in logical cluster %s: %v", i.location, i.logicalClusterName, err)
	}
	for _, obj := range objs {
		apiResourceImportToDelete := obj.(*apiresourcev1alpha1.APIResourceImport)
		err := i.kcpClusterClient.Cluster(i.logicalClusterName).ApiresourceV1alpha1().APIResourceImports().Delete(request.WithCluster(context.Background(), request.Cluster{Name: i.logicalClusterName}), apiResourceImportToDelete.Name, metav1.DeleteOptions{})
		if err != nil {
			klog.Errorf("error deleting APIResourceImport %s: %v", apiResourceImportToDelete.Name, err)
		}
	}
}

func (i *APIImporter) ImportAPIs(ctx context.Context) {
	klog.Infof("Importing APIs from location %s in logical cluster %s (resources=%v)", i.location, i.logicalClusterName, i.resourcesToSync)
	crds, err := i.schemaPuller.PullCRDs(ctx, i.resourcesToSync...)
	if err != nil {
		klog.Errorf("error pulling CRDs: %v", err)
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

		objs, err := i.apiresourceImportIndexer.ByIndex(
			clusterctl.GVRForLocationInLogicalClusterIndexName,
			clusterctl.GetGVRForLocationInLogicalClusterIndexKey(i.location, i.logicalClusterName, gvr),
		)
		if err != nil {
			klog.Errorf("error pulling CRDs: %v", err)
			continue
		}
		if len(objs) > 1 {
			klog.Errorf("There should be only one APIResourceImport of GVR %s for location %s in logical cluster %s, but there was %d", gvr.String(), i.location, i.logicalClusterName, len(objs))
			continue
		}
		if len(objs) == 1 {
			apiResourceImport := objs[0].(*apiresourcev1alpha1.APIResourceImport).DeepCopy()
			if err := apiResourceImport.Spec.SetSchema(crdVersion.Schema.OpenAPIV3Schema); err != nil {
				klog.Errorf("Error setting schema: %v", err)
				continue
			}
			klog.Infof("Updating APIResourceImport %s|%s for SyncTarget %s", i.logicalClusterName, apiResourceImport.Name, i.location)
			if _, err := i.kcpClusterClient.Cluster(i.logicalClusterName).ApiresourceV1alpha1().APIResourceImports().Update(ctx, apiResourceImport, metav1.UpdateOptions{}); err != nil {
				klog.Errorf("error updating APIResourceImport %s: %v", apiResourceImport.Name, err)
				continue
			}
		} else {
			apiResourceImportName := gvr.Resource + "." + i.location + "." + gvr.Version + "."
			if gvr.Group == "" {
				apiResourceImportName = apiResourceImportName + "core"
			} else {
				apiResourceImportName = apiResourceImportName + gvr.Group
			}

			clusterKey, err := cache.MetaNamespaceKeyFunc(&metav1.PartialObjectMetadata{
				ObjectMeta: metav1.ObjectMeta{
					Name: i.location,
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: i.logicalClusterName.String(),
					},
				},
			})
			if err != nil {
				klog.Errorf("error creating APIResourceImport %s: %v", apiResourceImportName, err)
				continue
			}
			clusterObj, exists, err := i.clusterIndexer.GetByKey(clusterKey)
			if err != nil {
				klog.Errorf("error creating APIResourceImport %s: %v", apiResourceImportName, err)
				continue
			}
			if !exists {
				klog.Errorf("error creating APIResourceImport %s: the cluster object should exist in the index for location %s in logical cluster %s", apiResourceImportName, i.location, i.logicalClusterName)
				continue
			}
			cluster, isCluster := clusterObj.(*workloadv1alpha1.SyncTarget)
			if !isCluster {
				klog.Errorf("error creating APIResourceImport %s: the object retrieved from the cluster index for location %s in logical cluster %s should be a cluster object, but is of type: %T", apiResourceImportName, i.location, i.logicalClusterName, clusterObj)
				continue
			}
			groupVersion := apiresourcev1alpha1.GroupVersion{
				Group:   gvr.Group,
				Version: gvr.Version,
			}
			apiResourceImport := &apiresourcev1alpha1.APIResourceImport{
				ObjectMeta: metav1.ObjectMeta{
					Name: apiResourceImportName,
					OwnerReferences: []metav1.OwnerReference{
						clusterAsOwnerReference(cluster, true),
					},
					Annotations: map[string]string{
						logicalcluster.AnnotationKey:             i.logicalClusterName.String(),
						apiresourcev1alpha1.APIVersionAnnotation: groupVersion.APIVersion(),
					},
				},
				Spec: apiresourcev1alpha1.APIResourceImportSpec{
					Location:             i.location,
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
				klog.Errorf("Error setting schema: %v", err)
				continue
			}
			if value, found := pulledCrd.Annotations[apiextensionsv1.KubeAPIApprovedAnnotation]; found {
				apiResourceImport.Annotations[apiextensionsv1.KubeAPIApprovedAnnotation] = value
			}

			klog.Infof("Creating APIResourceImport %s|%s", i.logicalClusterName, apiResourceImportName)
			if _, err := i.kcpClusterClient.Cluster(i.logicalClusterName).ApiresourceV1alpha1().APIResourceImports().Create(ctx, apiResourceImport, metav1.CreateOptions{}); err != nil {
				klog.Errorf("error creating APIResourceImport %s: %v", apiResourceImport.Name, err)
				continue
			}
		}
		gvrsToSync[gvr.String()] = gvr
	}

	gvrsToRemove := sets.StringKeySet(i.SyncedGVRs).Difference(sets.StringKeySet(gvrsToSync))
	for _, gvrToRemove := range gvrsToRemove.UnsortedList() {
		gvr := i.SyncedGVRs[gvrToRemove]
		objs, err := i.apiresourceImportIndexer.ByIndex(
			clusterctl.GVRForLocationInLogicalClusterIndexName,
			clusterctl.GetGVRForLocationInLogicalClusterIndexKey(i.location, i.logicalClusterName, gvr),
		)
		if err != nil {
			klog.Errorf("error pulling CRDs: %v", err)
			continue
		}
		if len(objs) > 1 {
			klog.Errorf("There should be only one APIResourceImport of GVR %s for location %s in logical cluster %s, but there was %d", gvr.String(), i.location, i.logicalClusterName, len(objs))
			continue
		}
		if len(objs) == 1 {
			apiResourceImportToRemove := objs[0].(*apiresourcev1alpha1.APIResourceImport)
			klog.Infof("Deleting APIResourceImport %s|%s", i.logicalClusterName, apiResourceImportToRemove.Name)
			err := i.kcpClusterClient.Cluster(i.logicalClusterName).ApiresourceV1alpha1().APIResourceImports().Delete(ctx, apiResourceImportToRemove.Name, metav1.DeleteOptions{})
			if err != nil {
				klog.Errorf("error deleting APIResourceImport %s: %v", apiResourceImportToRemove.Name, err)
				continue
			}
		}
	}
}
