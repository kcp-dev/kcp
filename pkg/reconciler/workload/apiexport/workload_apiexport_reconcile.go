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

package apiexport

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apiresource/v1alpha1"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
)

type reconcileStatus int

const (
	reconcileStatusStop reconcileStatus = iota
	reconcileStatusContinue
)

type reconciler interface {
	reconcile(ctx context.Context, export *apisv1alpha1.APIExport) (reconcileStatus, error)
}

type schemaReconciler struct {
	listNegotiatedAPIResources func(clusterName logicalcluster.Name) ([]*apiresourcev1alpha1.NegotiatedAPIResource, error)
	listAPIResourceSchemas     func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIResourceSchema, error)
	listSyncTargets            func(clusterName logicalcluster.Name) ([]*workloadv1alpha1.SyncTarget, error)
	getAPIResourceSchema       func(ctx context.Context, clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
	createAPIResourceSchema    func(ctx context.Context, clusterName logicalcluster.Path, schema *apisv1alpha1.APIResourceSchema) (*apisv1alpha1.APIResourceSchema, error)
	deleteAPIResourceSchema    func(ctx context.Context, clusterName logicalcluster.Path, name string) error
	updateAPIExport            func(ctx context.Context, clusterName logicalcluster.Path, export *apisv1alpha1.APIExport) (*apisv1alpha1.APIExport, error)

	enqueueAfter func(*apisv1alpha1.APIExport, time.Duration)
}

func (r *schemaReconciler) reconcile(ctx context.Context, export *apisv1alpha1.APIExport) (reconcileStatus, error) {
	logger := klog.FromContext(ctx)
	clusterName := logicalcluster.From(export)

	if export.Name != workloadv1alpha1.ImportedAPISExportName {
		return reconcileStatusStop, nil
	}

	resources, err := r.listNegotiatedAPIResources(clusterName)
	if err != nil {
		return reconcileStatusStop, err
	}
	if len(resources) == 0 {
		// ignore this export. Compare with TODO above about identfication.
		return reconcileStatusStop, nil
	}

	resourcesByResourceGroup := map[schema.GroupResource]*apiresourcev1alpha1.NegotiatedAPIResource{}
	for _, r := range resources {
		// TODO(sttts): what about multiple versions of the same resource? Something is missing in the apiresource APIs and controllers to support that.
		gr := schema.GroupResource{
			Group:    r.Spec.GroupVersion.Group,
			Resource: r.Spec.Plural,
		}
		if gr.Group == "" {
			gr.Group = "core"
		}
		resourcesByResourceGroup[gr] = r
	}

	// reconcile schemas in export
	// we check all schemas reference in the apiexport. if it is missing, we should create the schema
	// if it is outdated, we should create the schema and delete the outdated schema.
	upToDateResourceGroups := sets.NewString()
	expectedResourceGroups := sets.NewString()

	// schemaNamesByResourceGroup records the up-to-date APIResourceSchemaName for the resourceGroup, and the
	// APIExport will be updated accordingly
	schemaNamesByResourceGroup := map[schema.GroupResource]string{}
	for _, schemaName := range export.Spec.LatestResourceSchemas {
		gr, ok := ParseAPIResourceSchemaName(schemaName)
		if !ok {
			continue
		}
		expectedResourceGroups.Insert(gr.String())
		schemaNamesByResourceGroup[gr] = schemaName

		existingSchema, err := r.getAPIResourceSchema(ctx, clusterName, schemaName)
		if apierrors.IsNotFound(err) {
			// not found, will be recreated
			continue
		} else if err != nil {
			return reconcileStatusStop, err
		}

		// negotiated schema gone?
		negotiated, ok := resourcesByResourceGroup[gr]
		if !ok {
			continue
		}

		// negotiated schema still matches APIResourceSchema?
		newSchema := toAPIResourceSchema(negotiated, "")
		if equality.Semantic.DeepEqual(&existingSchema.Spec, &newSchema.Spec) {
			// nothing to do
			upToDateResourceGroups.Insert(gr.String())
		}
	}

	// create missing or outdated schemas
	outdatedOrMissing := expectedResourceGroups.Difference(upToDateResourceGroups)
	outDatedSchemaNames := sets.NewString()
	for _, resourceGroup := range outdatedOrMissing.List() {
		logger.WithValues("schema", resourceGroup).V(2).Info("missing or outdated schema on APIExport, adding")
		gr := schema.ParseGroupResource(resourceGroup)
		resource, ok := resourcesByResourceGroup[gr]
		if !ok {
			// no negotiated schema, keep the current schema name.
			continue
		}

		group := resource.Spec.GroupVersion.Group
		if group == "" {
			group = "core"
		}
		schemaName := fmt.Sprintf("rev-%s.%s.%s", resource.ResourceVersion, resource.Spec.Plural, group)
		schema := toAPIResourceSchema(resource, schemaName)
		schema.OwnerReferences = []metav1.OwnerReference{
			*metav1.NewControllerRef(export, apisv1alpha1.SchemeGroupVersion.WithKind("APIExport")),
		}
		schema, err = r.createAPIResourceSchema(ctx, clusterName.Path(), schema)
		if apierrors.IsAlreadyExists(err) {
			schema, err = r.getAPIResourceSchema(ctx, clusterName, schemaName)
		}
		if err != nil {
			return reconcileStatusStop, err
		}

		if _, ok := schemaNamesByResourceGroup[gr]; ok {
			outDatedSchemaNames.Insert(schemaNamesByResourceGroup[gr])
		}
		schemaNamesByResourceGroup[gr] = schema.Name
	}

	// update schema list in export
	old := export.DeepCopy()
	export.Spec.LatestResourceSchemas = []string{}
	for _, schemaName := range schemaNamesByResourceGroup {
		export.Spec.LatestResourceSchemas = append(export.Spec.LatestResourceSchemas, schemaName)
	}
	if !reflect.DeepEqual(old.Spec.LatestResourceSchemas, export.Spec.LatestResourceSchemas) {
		if _, err := r.updateAPIExport(ctx, clusterName.Path(), export); err != nil {
			return reconcileStatusStop, err
		}
	}

	// delete schemas that are no longer needed
	for schemaName := range outDatedSchemaNames {
		logger.V(2).Info("deleting schema of APIExport", "APIResourceSchema", schemaName)
		if err := r.deleteAPIResourceSchema(ctx, clusterName.Path(), schemaName); err != nil && !apierrors.IsNotFound(err) {
			return reconcileStatusStop, err
		}
	}

	return reconcileStatusContinue, nil
}

func (c *controller) reconcile(ctx context.Context, export *apisv1alpha1.APIExport) error {
	reconcilers := []reconciler{
		&schemaReconciler{
			listNegotiatedAPIResources: c.listNegotiatedAPIResources,
			listAPIResourceSchemas:     c.listAPIResourceSchemas,
			listSyncTargets:            c.listSyncTarget,
			getAPIResourceSchema:       c.getAPIResourceSchema,
			createAPIResourceSchema:    c.createAPIResourceSchema,
			deleteAPIResourceSchema:    c.deleteAPIResourceSchema,
			updateAPIExport:            c.updateAPIExport,
			enqueueAfter:               c.enqueueAfter,
		},
	}

	var errs []error

	for _, r := range reconcilers {
		status, err := r.reconcile(ctx, export)
		if err != nil {
			errs = append(errs, err)
		}
		if status == reconcileStatusStop {
			break
		}
	}

	return errors.NewAggregate(errs)
}

func (c *controller) listNegotiatedAPIResources(clusterName logicalcluster.Name) ([]*apiresourcev1alpha1.NegotiatedAPIResource, error) {
	return c.negotiatedAPIResourceLister.Cluster(clusterName).List(labels.Everything())
}

func (c *controller) listAPIResourceSchemas(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIResourceSchema, error) {
	return c.apiResourceSchemaLister.Cluster(clusterName).List(labels.Everything())
}

func (c *controller) listSyncTarget(clusterName logicalcluster.Name) ([]*workloadv1alpha1.SyncTarget, error) {
	return c.syncTargetClusterLister.Cluster(clusterName).List(labels.Everything())
}

func (c *controller) getAPIResourceSchema(ctx context.Context, clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
	schema, err := c.apiResourceSchemaLister.Cluster(clusterName).Get(name)
	if apierrors.IsNotFound(err) {
		return c.kcpClusterClient.Cluster(clusterName.Path()).ApisV1alpha1().APIResourceSchemas().Get(ctx, name, metav1.GetOptions{})
	}
	return schema, err
}

func (c *controller) createAPIResourceSchema(ctx context.Context, clusterName logicalcluster.Path, schema *apisv1alpha1.APIResourceSchema) (*apisv1alpha1.APIResourceSchema, error) {
	return c.kcpClusterClient.Cluster(clusterName).ApisV1alpha1().APIResourceSchemas().Create(ctx, schema, metav1.CreateOptions{})
}

func (c *controller) updateAPIExport(ctx context.Context, clusterName logicalcluster.Path, export *apisv1alpha1.APIExport) (*apisv1alpha1.APIExport, error) {
	return c.kcpClusterClient.Cluster(clusterName).ApisV1alpha1().APIExports().Update(ctx, export, metav1.UpdateOptions{})
}

func (c *controller) deleteAPIResourceSchema(ctx context.Context, clusterName logicalcluster.Path, name string) error {
	return c.kcpClusterClient.Cluster(clusterName).ApisV1alpha1().APIResourceSchemas().Delete(ctx, name, metav1.DeleteOptions{})
}
