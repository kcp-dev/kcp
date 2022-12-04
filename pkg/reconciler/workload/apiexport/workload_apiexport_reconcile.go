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
	"strings"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/config/rootcompute"
	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
)

type reconcileStatus int

const (
	reconcileStatusStop reconcileStatus = iota
	reconcileStatusContinue
)

// rootComputeResourceSchema are the APIResourceSchemas which should not be added into the APIExport. These are
// APIResourceSchemas of kubernetes in root:compute workspace
var rootComputeResourceSchema = sets.NewString(
	"deployments.apps",
	"services.core",
	"ingresses.networking.k8s.io",
)

type reconciler interface {
	reconcile(ctx context.Context, export *apisv1alpha1.APIExport) (reconcileStatus, error)
}

type schemaReconciler struct {
	listNegotiatedAPIResources func(clusterName logicalcluster.Name) ([]*apiresourcev1alpha1.NegotiatedAPIResource, error)
	listAPIResourceSchemas     func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIResourceSchema, error)
	listSyncTargets            func(clusterName logicalcluster.Name) ([]*workloadv1alpha1.SyncTarget, error)
	getAPIResourceSchema       func(ctx context.Context, clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
	createAPIResourceSchema    func(ctx context.Context, clusterName logicalcluster.Name, schema *apisv1alpha1.APIResourceSchema) (*apisv1alpha1.APIResourceSchema, error)
	deleteAPIResourceSchema    func(ctx context.Context, clusterName logicalcluster.Name, name string) error
	updateAPIExport            func(ctx context.Context, clusterName logicalcluster.Name, export *apisv1alpha1.APIExport) (*apisv1alpha1.APIExport, error)

	enqueueAfter func(*apisv1alpha1.APIExport, time.Duration)
}

func (r *schemaReconciler) reconcile(ctx context.Context, export *apisv1alpha1.APIExport) (reconcileStatus, error) {
	logger := klog.FromContext(ctx)
	clusterName := logicalcluster.From(export)

	if export.Name != TemporaryComputeServiceExportName {
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

	shouldSkip, err := r.shouldSkipComputeAPIs(clusterName)
	if err != nil {
		return reconcileStatusStop, err
	}

	// we expect schemas for all negotiated resources
	expectedResourceGroups := sets.NewString()
	resourcesByResourceGroup := map[string]*apiresourcev1alpha1.NegotiatedAPIResource{}
	for _, r := range resources {
		// TODO(sttts): what about multiple versions of the same resource? Something is missing in the apiresource APIs and controllers to support that.
		resource, _, group, ok := split3(r.Name, ".")
		if !ok {
			continue
		}
		schemaName := fmt.Sprintf("%s.%s", resource, group)

		// APIResourceSchemas already in root:compute should be skipped.
		if shouldSkip && rootComputeResourceSchema.Has(schemaName) {
			continue
		}

		expectedResourceGroups.Insert(schemaName)
		resourcesByResourceGroup[schemaName] = r
	}

	// reconcile schemas in export
	upToDate := sets.NewString()
	schemasByResourceGroup := map[string]*apisv1alpha1.APIResourceSchema{}
	for _, schemaName := range export.Spec.LatestResourceSchemas {
		_, resource, group, ok := split3(schemaName, ".")
		if !ok {
			continue
		}
		resourceGroup := fmt.Sprintf("%s.%s", resource, group)

		existingSchema, err := r.getAPIResourceSchema(ctx, clusterName, schemaName)
		if apierrors.IsNotFound(err) {
			// not found, will be recreated
			continue
		} else if err != nil {
			return reconcileStatusStop, err
		}

		// negotiated schema gone?
		negotiated, ok := resourcesByResourceGroup[resourceGroup]
		if !ok {
			// will be deleted at the end
			continue
		}

		// negotiated schema still matches APIResourceSchema?
		newSchema := toAPIResourceSchema(negotiated, "")
		if equality.Semantic.DeepEqual(&existingSchema.Spec, &newSchema.Spec) {
			// nothing to do
			upToDate.Insert(resourceGroup)
			schemasByResourceGroup[resourceGroup] = existingSchema
		}
	}

	// create missing or outdated schemas
	outdatedOrMissing := expectedResourceGroups.Difference(upToDate)
	for _, resourceGroup := range outdatedOrMissing.List() {
		logger.WithValues("schema", resourceGroup).V(2).Info("missing or outdated schema on APIExport, adding")
		resource := resourcesByResourceGroup[resourceGroup]

		group := resource.Spec.GroupVersion.Group
		if group == "" {
			group = "core"
		}
		schemaName := fmt.Sprintf("rev-%s.%s.%s", resource.ResourceVersion, resource.Spec.Plural, group)
		schema := toAPIResourceSchema(resource, schemaName)
		schema.OwnerReferences = []metav1.OwnerReference{
			*metav1.NewControllerRef(export, apisv1alpha1.SchemeGroupVersion.WithKind("APIExport")),
		}
		schema, err = r.createAPIResourceSchema(ctx, clusterName, schema)
		if apierrors.IsAlreadyExists(err) {
			schema, err = r.getAPIResourceSchema(ctx, clusterName, schemaName)
		}
		if err != nil {
			return reconcileStatusStop, err
		}

		schemasByResourceGroup[resourceGroup] = schema
	}

	// update schema list in export
	old := export.DeepCopy()
	export.Spec.LatestResourceSchemas = []string{}
	referencedSchemaNames := map[string]bool{}
	for _, resourceGroup := range expectedResourceGroups.List() {
		schema, ok := schemasByResourceGroup[resourceGroup]
		if !ok {
			// should not happen. We should have all schemas by now
			logger.WithValues("schema", resourceGroup).Error(fmt.Errorf("schema for resource %q not found", resourceGroup), "unexpectedly missing schema for resource in APIExport")
			return reconcileStatusStop, nil
		}

		export.Spec.LatestResourceSchemas = append(export.Spec.LatestResourceSchemas, schema.Name)
		referencedSchemaNames[schema.Name] = true
	}
	if !reflect.DeepEqual(old.Spec.LatestResourceSchemas, export.Spec.LatestResourceSchemas) {
		if _, err := r.updateAPIExport(ctx, clusterName, export); err != nil {
			return reconcileStatusStop, err
		}
	}

	// delete schemas that are no longer needed
	allSchemas, err := r.listAPIResourceSchemas(clusterName)
	if err != nil {
		return reconcileStatusStop, err
	}
	for _, schema := range allSchemas {
		if !referencedSchemaNames[schema.Name] && metav1.IsControlledBy(schema, export) {
			logging.WithObject(logger, schema).V(2).Info("deleting schema of APIExport")
			if err := r.deleteAPIResourceSchema(ctx, clusterName, schema.Name); err != nil && !apierrors.IsNotFound(err) {
				return reconcileStatusStop, err
			}
		}
	}

	return reconcileStatusContinue, nil
}

func (r *schemaReconciler) shouldSkipComputeAPIs(clusterName logicalcluster.Name) (bool, error) {
	syncTargets, err := r.listSyncTargets(clusterName)
	if err != nil {
		return false, err
	}

	for _, syncTarget := range syncTargets {
		for _, export := range syncTarget.Spec.SupportedAPIExports {
			if export.Cluster == nil {
				continue
			}
			// TODO: this does not work. We must not handle root:compute special in any way.
			if export.Cluster.ExportName == TemporaryComputeServiceExportName && export.Cluster.Path == rootcompute.RootComputeWorkspace.String() {
				return true, nil
			}
		}
	}

	return false, nil
}

func split3(s string, sep string) (string, string, string, bool) {
	comps := strings.SplitN(s, sep, 3)
	if len(comps) != 3 {
		return "", "", "", false
	}
	return comps[0], comps[1], comps[2], true
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
		return c.kcpClusterClient.Cluster(clusterName).ApisV1alpha1().APIResourceSchemas().Get(ctx, name, metav1.GetOptions{})
	}
	return schema, err
}

func (c *controller) createAPIResourceSchema(ctx context.Context, clusterName logicalcluster.Name, schema *apisv1alpha1.APIResourceSchema) (*apisv1alpha1.APIResourceSchema, error) {
	return c.kcpClusterClient.Cluster(clusterName).ApisV1alpha1().APIResourceSchemas().Create(ctx, schema, metav1.CreateOptions{})
}

func (c *controller) updateAPIExport(ctx context.Context, clusterName logicalcluster.Name, export *apisv1alpha1.APIExport) (*apisv1alpha1.APIExport, error) {
	return c.kcpClusterClient.Cluster(clusterName).ApisV1alpha1().APIExports().Update(ctx, export, metav1.UpdateOptions{})
}

func (c *controller) deleteAPIResourceSchema(ctx context.Context, clusterName logicalcluster.Name, name string) error {
	return c.kcpClusterClient.Cluster(clusterName).ApisV1alpha1().APIResourceSchemas().Delete(ctx, name, metav1.DeleteOptions{})
}
