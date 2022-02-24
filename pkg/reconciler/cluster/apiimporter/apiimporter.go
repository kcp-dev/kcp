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

package apiimporter

import (
	"context"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/pkg/crdpuller"
	clusterctl "github.com/kcp-dev/kcp/pkg/reconciler/cluster"
)

var clusterKind = reflect.TypeOf(clusterv1alpha1.Cluster{}).Name()

func ClusterAsOwnerReference(obj *clusterv1alpha1.Cluster, controller bool) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: apiresourcev1alpha1.SchemeGroupVersion.String(),
		Kind:       clusterKind,
		Name:       obj.Name,
		UID:        obj.UID,
		Controller: &controller,
	}
}

type APIImporter struct {
	kcpClusterClient         *kcpclient.Cluster
	resourcesToSync          []string
	apiresourceImportIndexer cache.Indexer
	clusterIndexer           cache.Indexer

	location           string
	logicalClusterName string
	schemaPuller       crdpuller.SchemaPuller
	done               chan bool
	SyncedGVRs         map[string]metav1.GroupVersionResource
	context            context.Context
}

func (i *APIImporter) Stop() {
	klog.Infof("Stopping API Importer for location %s in cluster %s", i.location, i.logicalClusterName)

	i.done <- true

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

func (i *APIImporter) ImportAPIs() {
	klog.Infof("Importing APIs from location %s in logical cluster %s", i.location, i.logicalClusterName)
	crds, err := i.schemaPuller.PullCRDs(i.context, i.resourcesToSync...)
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
			klog.Errorf("There should be only one APIResourceImport of GVR %s for location %s in logical cluster %s, but where was %d", gvr.String(), i.location, i.logicalClusterName, len(objs))
			continue
		}
		if len(objs) == 1 {
			apiResourceImport := objs[0].(*apiresourcev1alpha1.APIResourceImport).DeepCopy()
			if err := apiResourceImport.Spec.SetSchema(crdVersion.Schema.OpenAPIV3Schema); err != nil {
				klog.Errorf("Error setting schema: %v", err)
				continue
			}
			if _, err := i.kcpClusterClient.Cluster(i.logicalClusterName).ApiresourceV1alpha1().APIResourceImports().Update(i.context, apiResourceImport, metav1.UpdateOptions{}); err != nil {
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
					Name:        i.location,
					ClusterName: i.logicalClusterName,
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
			cluster, isCluster := clusterObj.(*clusterv1alpha1.Cluster)
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
					Name:        apiResourceImportName,
					ClusterName: i.logicalClusterName,
					OwnerReferences: []metav1.OwnerReference{
						ClusterAsOwnerReference(cluster, true),
					},
					Annotations: map[string]string{
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
			if _, err := i.kcpClusterClient.Cluster(i.logicalClusterName).ApiresourceV1alpha1().APIResourceImports().Create(i.context, apiResourceImport, metav1.CreateOptions{}); err != nil {
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
			err := i.kcpClusterClient.Cluster(i.logicalClusterName).ApiresourceV1alpha1().APIResourceImports().Delete(i.context, apiResourceImportToRemove.Name, metav1.DeleteOptions{})
			if err != nil {
				klog.Errorf("error deleting APIResourceImport %s: %v", apiResourceImportToRemove.Name, err)
				continue
			}
		}
	}
}
