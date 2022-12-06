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

	"github.com/kcp-dev/logicalcluster/v3"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsv1client "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpclusterclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	apiresourceinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apiresource/v1alpha1"
	workloadinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/workload/v1alpha1"
	workloadv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/crdpuller"
	"github.com/kcp-dev/kcp/pkg/logging"
)

var clusterKind = reflect.TypeOf(workloadv1alpha1.SyncTarget{}).Name()

const GVRForLocationIndexName = "GVRForLocation"

func GetGVRForLocationIndexKey(location string, gvr metav1.GroupVersionResource) string {
	return location + "$$" + gvr.String()
}

const LocationIndexName = "Location"

func GetLocationIndexKey(location string) string {
	return location
}

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
	synctargetInformer workloadinformers.SyncTargetInformer,
	apiImportInformer apiresourceinformer.APIResourceImportInformer,
	resourcesToSync []string,
	logicalClusterName logicalcluster.Path,
	syncTargetName string,
	syncTargetUID types.UID,
) (*APIImporter, error) {
	agent := fmt.Sprintf("kcp-workload-api-importer-%s-%s", logicalClusterName, syncTargetName)
	upstreamConfig = rest.AddUserAgent(rest.CopyConfig(upstreamConfig), agent)
	downstreamConfig = rest.AddUserAgent(rest.CopyConfig(downstreamConfig), agent)

	kcpClusterClient, err := kcpclusterclientset.NewForConfig(upstreamConfig)
	if err != nil {
		return nil, err
	}

	kcpClient := kcpClusterClient.Cluster(logicalClusterName)

	importIndexer := apiImportInformer.Informer().GetIndexer()

	indexers := map[string]cache.IndexFunc{
		GVRForLocationIndexName: func(obj interface{}) ([]string, error) {
			if apiResourceImport, ok := obj.(*apiresourcev1alpha1.APIResourceImport); ok {
				return []string{GetGVRForLocationIndexKey(apiResourceImport.Spec.Location, apiResourceImport.GVR())}, nil
			}
			return []string{}, nil
		},
		LocationIndexName: func(obj interface{}) ([]string, error) {
			if apiResourceImport, ok := obj.(*apiresourcev1alpha1.APIResourceImport); ok {
				return []string{GetLocationIndexKey(apiResourceImport.Spec.Location)}, nil
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

	crdClient, err := apiextensionsv1client.NewForConfig(downstreamConfig)
	if err != nil {
		return nil, fmt.Errorf("error creating downstream apiextensions client: %w", err)
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(downstreamConfig)
	if err != nil {
		return nil, fmt.Errorf("error creating downstream discovery client: %w", err)
	}

	schemaPuller, err := crdpuller.NewSchemaPuller(discoveryClient, crdClient)
	if err != nil {
		return nil, err
	}

	return &APIImporter{
		kcpClient:                kcpClient,
		resourcesToSync:          resourcesToSync,
		apiresourceImportIndexer: importIndexer,
		syncTargetLister:         synctargetInformer.Lister(),

		syncTargetName: syncTargetName,
		syncTargetUID:  syncTargetUID,
		schemaPuller:   schemaPuller,
	}, nil
}

type APIImporter struct {
	kcpClient                kcpclientset.Interface
	resourcesToSync          []string
	apiresourceImportIndexer cache.Indexer
	syncTargetLister         workloadv1alpha1listers.SyncTargetLister

	syncTargetName string
	syncTargetUID  types.UID
	schemaPuller   schemaPuller
	SyncedGVRs     map[string]metav1.GroupVersionResource
}

// schemaPuller allows pulling the API resources as CRDs
// from a kubernetes cluster.
type schemaPuller interface {
	// PullCRDs allows pulling the resources named by their plural names
	// and make them available as CRDs in the output map.
	PullCRDs(context context.Context, resourceNames ...string) (map[schema.GroupResource]*apiextensionsv1.CustomResourceDefinition, error)
}

func (i *APIImporter) Start(ctx context.Context, pollInterval time.Duration) {
	defer runtime.HandleCrash()

	logger := logging.WithReconciler(klog.FromContext(ctx), "api-importer")
	ctx = klog.NewContext(ctx, logger)

	logger.Info("Starting API Importer")

	go wait.UntilWithContext(ctx, func(innerCtx context.Context) {
		i.ImportAPIs(innerCtx)
	}, pollInterval)

	<-ctx.Done()
	i.Stop(ctx)
}

func (i *APIImporter) Stop(ctx context.Context) {
	logger := klog.FromContext(ctx)
	logger.Info("stopping API Importer")

	objs, err := i.apiresourceImportIndexer.ByIndex(
		LocationIndexName,
		GetLocationIndexKey(i.syncTargetName),
	)
	if err != nil {
		logger.Error(err, "error trying to list APIResourceImport objects")
	}
	for _, obj := range objs {
		apiResourceImportToDelete := obj.(*apiresourcev1alpha1.APIResourceImport)
		logger := logger.WithValues("apiResourceImport", apiResourceImportToDelete.Name)
		err := i.kcpClient.ApiresourceV1alpha1().APIResourceImports().Delete(context.Background(), apiResourceImportToDelete.Name, metav1.DeleteOptions{})
		if err != nil {
			logger.Error(err, "error deleting APIResourceImport")
		}
	}
}

func (i *APIImporter) ImportAPIs(ctx context.Context) {
	logger := klog.FromContext(ctx)

	syncTarget, err := i.syncTargetLister.Get(i.syncTargetName)

	if err != nil {
		logger.Error(err, "error getting syncTarget")
		return
	}

	if syncTarget.GetUID() != i.syncTargetUID {
		logger.Error(fmt.Errorf("syncTarget uid is not correct, current: %s, required: %s", syncTarget.GetUID(), i.syncTargetUID), "error getting syncTarget")
		return
	}

	// merge resourceToSync from synctarget with resourcesToSync set on the syncer's flag.
	resourceToSyncSet := sets.NewString(i.resourcesToSync...)
	for _, rs := range syncTarget.Status.SyncedResources {
		resourceToSyncSet.Insert(fmt.Sprintf("%s.%s", rs.Resource, rs.Group))
	}
	// return if no resources to import
	resourcesToSync := resourceToSyncSet.List()
	logger.V(2).Info("Importing APIs", "resourcesToImport", resourcesToSync)
	if resourceToSyncSet.Len() == 0 {
		return
	}

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
			GVRForLocationIndexName,
			GetGVRForLocationIndexKey(i.syncTargetName, gvr),
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
			if _, err := i.kcpClient.ApiresourceV1alpha1().APIResourceImports().Update(ctx, apiResourceImport, metav1.UpdateOptions{}); err != nil {
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
						clusterAsOwnerReference(syncTarget, true),
					},
					Annotations: map[string]string{
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
			if _, err := i.kcpClient.ApiresourceV1alpha1().APIResourceImports().Create(ctx, apiResourceImport, metav1.CreateOptions{}); err != nil {
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
			GVRForLocationIndexName,
			GetGVRForLocationIndexKey(i.syncTargetName, gvr),
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
			err := i.kcpClient.ApiresourceV1alpha1().APIResourceImports().Delete(ctx, apiResourceImportToRemove.Name, metav1.DeleteOptions{})
			if err != nil {
				logger.Error(err, "error deleting APIResourceImport")
				continue
			}
		}
	}
}
