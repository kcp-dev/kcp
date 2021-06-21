package cluster

import (
	"context"
	"time"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/crdpuller"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"
)

type APIImporter struct {
	c                  *Controller
	location           string
	logicalClusterName string
	schemaPuller       crdpuller.SchemaPuller
	done               chan bool
	SyncedGVRs         map[string]metav1.GroupVersionResource
	context            context.Context
}

func (c *Controller) StartAPIImporter(config *rest.Config, location string, logicalClusterName string, pollInterval time.Duration) (*APIImporter, error) {
	apiImporter := APIImporter{
		c:                  c,
		location:           location,
		logicalClusterName: logicalClusterName,
		context:            request.WithCluster(context.Background(), request.Cluster{Name: logicalClusterName}),
	}

	ticker := time.NewTicker(1 * time.Minute)
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

func (i *APIImporter) Stop() {
	i.done <- true

	objs, err := i.c.apiresourceImportIndexer.ByIndex(LocationInLogicalClusterIndexName, GetLocationInLogicalClusterIndexKey(i.location, i.logicalClusterName))
	if err != nil {
		klog.Errorf("error trying to list APIResourceImport objects for location %s in logical cluster %s: %v", i.location, i.logicalClusterName, err)
	}
	for _, obj := range objs {
		apiResourceImportToDelete := obj.(*apiresourcev1alpha1.APIResourceImport)
		err := i.c.apiResourceClient.APIResourceImports().Delete(request.WithCluster(context.Background(), request.Cluster{Name: i.logicalClusterName}), apiResourceImportToDelete.Name, metav1.DeleteOptions{})
		if err != nil {
			klog.Errorf("error deleting APIResourceImport %s: %v", apiResourceImportToDelete.Name, err)
		}
	}
}

func (i *APIImporter) ImportAPIs() {
	crds, err := i.schemaPuller.PullCRDs(i.context, i.c.resourcesToSync...)
	if err != nil {
		klog.Errorf("error pulling CRDs: %v", err)
	}

	gvrsToSync := map[string]metav1.GroupVersionResource{}
	for groupResource, pulledCrd := range crds {
		crdVersion := pulledCrd.Spec.Versions[0]
		gvr := metav1.GroupVersionResource{
			Group:    pulledCrd.Spec.Group,
			Version:  crdVersion.Name,
			Resource: groupResource.Resource,
		}

		objs, err := i.c.apiresourceImportIndexer.ByIndex(GVRForLocationInLogicalClusterIndexName, GetGVRForLocationInLogicalClusterIndexKey(i.location, i.logicalClusterName, gvr))
		if err != nil {
			klog.Errorf("error pulling CRDs: %v", err)
			continue
		}
		if len(objs) > 1 {
			klog.Errorf("There should be only one APIResourceImport of GVR %s for location %s in logical cluster %s, but wthere was %d", gvr.String(), i.location, i.logicalClusterName, len(objs))
			continue
		}
		if len(objs) == 1 {
			apiResourceImport := objs[0].(*apiresourcev1alpha1.APIResourceImport)
			apiResourceImport.ClusterName = i.logicalClusterName
			apiResourceImport.Spec.SetSchema(crdVersion.Schema.OpenAPIV3Schema)
			_, err := i.c.apiResourceClient.APIResourceImports().Update(i.context, apiResourceImport, metav1.UpdateOptions{})
			if err != nil {
				if err != nil {
					klog.Errorf("error updating APIResourceImport %s: %v", apiResourceImport.Name, err)
					continue
				}
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
			clusterObj, exists, err := i.c.clusterIndexer.GetByKey(clusterKey)
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
			groupVersion :=  apiresourcev1alpha1.GroupVersion{
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
			apiResourceImport.Spec.SetSchema(crdVersion.Schema.OpenAPIV3Schema)
			if _, err := i.c.apiResourceClient.APIResourceImports().Create(i.context, apiResourceImport, metav1.CreateOptions{}); err != nil {
				klog.Errorf("error creating APIResourceImport %s: %v", apiResourceImport.Name, err)
				continue
			}
		}
		gvrsToSync[gvr.String()] = gvr
	}

	gvrsToRemove := sets.StringKeySet(i.SyncedGVRs).Difference(sets.StringKeySet(gvrsToSync))
	for _, gvrToRemove := range gvrsToRemove.UnsortedList() {
		gvr := i.SyncedGVRs[gvrToRemove]
		objs, err := i.c.apiresourceImportIndexer.ByIndex(GVRForLocationInLogicalClusterIndexName, GetGVRForLocationInLogicalClusterIndexKey(i.location, i.logicalClusterName, gvr))
		if err != nil {
			klog.Errorf("error pulling CRDs: %v", err)
			continue
		}
		if len(objs) > 1 {
			klog.Errorf("There should be only one APIResourceImport of GVR %s for location %s in logical cluster %s, but wthere was %d", gvr.String(), i.location, i.logicalClusterName, len(objs))
			continue
		}
		if len(objs) == 1 {
			apiResourceImportToRemove := objs[0].(*apiresourcev1alpha1.APIResourceImport)
			err := i.c.apiResourceClient.APIResourceImports().Delete(i.context, apiResourceImportToRemove.Name, metav1.DeleteOptions{})
			if err != nil {
				klog.Errorf("error deleting APIResourceImport %s: %v", apiResourceImportToRemove.Name, err)
				continue
			}
		}
	}
}
