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

package apiresource

import (
	"context"
	"errors"
	"reflect"

	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	crdhelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/klog/v2"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/schemacompat"
)

func (c *Controller) process(ctx context.Context, key queueElement) error {

	ctx = request.WithCluster(ctx, request.Cluster{Name: key.clusterName})

	switch key.theType {
	case customResourceDefinitionType:
		clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key.theKey)
		if err != nil {
			runtime.HandleError(err)
			return nil
		}
		crd, err := c.crdLister.Cluster(clusterName).Get(name)
		if err != nil {
			var deletedObjectExists bool
			crd, deletedObjectExists = key.deletedObject.(*apiextensionsv1.CustomResourceDefinition)
			if !k8serrors.IsNotFound(err) || !deletedObjectExists || crd == nil {
				return err
			}
		}
		switch key.theAction {
		case createdAction, specChangedAction:
			// - if no NegotiatedAPIResource owner
			// => Set the Enforced status condition, and then the schema of the Negotiated API Resource of each CRD version
			if c.isManuallyCreatedCRD(ctx, crd) {
				if err := c.enforceCRDToNegotiatedAPIResource(ctx, logicalcluster.From(crd), key.gvr, crd); err != nil {
					return err
				}
			}
			fallthrough
		case statusOnlyChangedAction:
			// Set the Enforced status on the related Negotiation resources
			// - if CRD is owned by a NegotiatedAPIResource
			// => Set the status (Published / Refused) on the Negotiated API Resource of each CRD version

			if err := c.updatePublishingStatusOnNegotiatedAPIResources(ctx, logicalcluster.From(crd), key.gvr, crd); err != nil {
				return err
			}
		case deletedAction:
			// - if no NegotiatedAPIResource owner
			// => Delete the Negotiated API Resource of each CRD version
			//    (they will be recreated from the related APIResourceImport objects, and if requested a CRD will be created again)

			if c.isManuallyCreatedCRD(ctx, crd) {
				return c.deleteNegotiatedAPIResource(ctx, logicalcluster.From(crd), key.gvr, crd)
			} else {
				return c.updatePublishingStatusOnNegotiatedAPIResources(ctx, logicalcluster.From(crd), key.gvr, crd)
			}
		}

	case apiResourceImportType:
		clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key.theKey)
		if err != nil {
			runtime.HandleError(err)
			return nil
		}
		apiResourceImport, err := c.apiResourceImportLister.Cluster(clusterName).Get(name)
		if err != nil {
			var deletedObjectExists bool
			apiResourceImport, deletedObjectExists = key.deletedObject.(*apiresourcev1alpha1.APIResourceImport)
			if !k8serrors.IsNotFound(err) || !deletedObjectExists || apiResourceImport == nil {
				return err
			}
		}
		switch key.theAction {
		case createdAction, specChangedAction:
			// - if strategy allows schema update of the negotiated API resource (and current negotiated API resource is not enforced)
			// => Calculate the LCD of this APIResourceImport schema against the schema of the corresponding NegotiatedAPIResource. If not errors occur
			//    update the NegotiatedAPIResource schema. Update the current APIResourceImport status accordingly (possibly reporting errors).
			// - else (NegotiatedAPIResource schema update is not allowed)
			// => Just check the compatibility of this APIResourceImport schema against the schema of the corresponding NegotiatedAPIResource.
			//    Update the current APIResourceImport status accordingly (possibly reporting errors).
			return c.ensureAPIResourceCompatibility(ctx, logicalcluster.From(apiResourceImport), key.gvr, apiResourceImport, "")
		case statusOnlyChangedAction:
			compatible := apiResourceImport.FindCondition(apiresourcev1alpha1.Compatible)
			available := apiResourceImport.FindCondition(apiresourcev1alpha1.Available)

			// - if both Compatible and Available conditions are unknown
			// => Do the same as if the APIResourceImport was just created or modified.
			if compatible == nil && available == nil {
				return c.ensureAPIResourceCompatibility(ctx, logicalcluster.From(apiResourceImport), key.gvr, apiResourceImport, "")
			}
		case deletedAction:
			// - If there is no other APIResourceImport for this GVR and the current negotiated API resource is not enforced
			// => Delete the corresponding NegotiatedAPIResource
			isOrphan, err := c.negotiatedAPIResourceIsOrphan(ctx, logicalcluster.From(apiResourceImport), key.gvr)
			if err != nil {
				return err
			}
			if isOrphan {
				return c.deleteNegotiatedAPIResource(ctx, logicalcluster.From(apiResourceImport), key.gvr, nil)
			}

			// - if strategy allows schema update of the negotiated API resource (and current negotiated API resource is not enforced)
			// => Calculate the LCD of all other APIResourceImports for this GVR and update the schema of the corresponding NegotiatedAPIResource.
			return c.ensureAPIResourceCompatibility(ctx, logicalcluster.From(apiResourceImport), key.gvr, nil, apiresourcev1alpha1.UpdatePublished)
		}
	case negotiatedAPIResourceType:
		clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key.theKey)
		if err != nil {
			runtime.HandleError(err)
			return nil
		}
		negotiatedApiResource, err := c.negotiatedApiResourceLister.Cluster(clusterName).Get(name)
		if err != nil {
			var deletedObjectExists bool
			negotiatedApiResource, deletedObjectExists = key.deletedObject.(*apiresourcev1alpha1.NegotiatedAPIResource)
			if !k8serrors.IsNotFound(err) || !deletedObjectExists || negotiatedApiResource == nil {
				return err
			}
		}
		switch key.theAction {
		case createdAction, specChangedAction:
			// if status.Enforced
			// => Check the schema of all APIResourceImports for this GVR against the schema of the NegotiatedAPIResource, and update the
			//    status of each one with the right Compatible condition.

			if negotiatedApiResource.IsConditionTrue(apiresourcev1alpha1.Enforced) {
				if err := c.ensureAPIResourceCompatibility(ctx, logicalcluster.From(negotiatedApiResource), key.gvr, nil, apiresourcev1alpha1.UpdateNever); err != nil {
					return err
				}
			}

			// if spec.Published && !status.Enforced
			// => If no CRD for the corresponding GVR exists
			//    => create it with the right CRD version that corresponds to the NegotiatedAPIResource spec content (schema included)
			//       and add the current NegotiatedAPIResource as owner of the CRD
			//    If the CRD for the corresponding GVR exists and has a NegotiatedAPIResource owner
			//    => update the CRD version of the existing CRD with the NegotiatedAPIResource spec content (schema included),
			//       and add the current NegotiatedAPIResource as owner of the CRD

			if negotiatedApiResource.Spec.Publish && !negotiatedApiResource.IsConditionTrue(apiresourcev1alpha1.Enforced) {
				if err := c.publishNegotiatedResource(ctx, logicalcluster.From(negotiatedApiResource), key.gvr, negotiatedApiResource); err != nil {
					return err
				}
			}
			fallthrough

		case statusOnlyChangedAction:
			// if status == Published
			// => Udate the status of related compatible APIResourceImports, to set the `Available` condition to `true`
			return c.updateStatusOnRelatedAPIResourceImports(ctx, logicalcluster.From(negotiatedApiResource), key.gvr, negotiatedApiResource)

		case deletedAction:
			// if a CRD with the same GV has a version == to the current NegotiatedAPIResource version *and* has the current object as owner:
			// => if this CRD version is the only one, then delete the CRD
			//    else remove this CRD version from the CRD, as well as the corresponding owner
			// In any case change the status on every APIResourceImport with the same GVR, to remove Compatible and Available conditions.
			return c.cleanupNegotiatedAPIResource(ctx, logicalcluster.From(negotiatedApiResource), key.gvr, negotiatedApiResource)
		}
	}

	return nil
}

var negotiatedAPIResourceKind string = reflect.TypeOf(apiresourcev1alpha1.NegotiatedAPIResource{}).Name()

func NegotiatedAPIResourceAsOwnerReference(obj *apiresourcev1alpha1.NegotiatedAPIResource) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: apiresourcev1alpha1.SchemeGroupVersion.String(),
		Kind:       negotiatedAPIResourceKind,
		Name:       obj.Name,
		UID:        obj.UID,
	}
}

// isManuallyCreatedCRD detects if a CRD was created manually.
// This can be deduced from the fact that it doesn't have any NegotiatedAPIResource owner reference
func (c *Controller) isManuallyCreatedCRD(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition) bool {
	for _, reference := range crd.OwnerReferences {
		if reference.APIVersion == apiresourcev1alpha1.SchemeGroupVersion.String() &&
			reference.Kind == negotiatedAPIResourceKind {
			return false
		}
	}
	return true
}

// enforceCRDToNegotiatedAPIResource sets the Enforced status condition,
// and then updates the schema of the Negotiated API Resource of each CRD version
func (c *Controller) enforceCRDToNegotiatedAPIResource(ctx context.Context, clusterName logicalcluster.Name, gvr metav1.GroupVersionResource, crd *apiextensionsv1.CustomResourceDefinition) error {
	logger := klog.FromContext(ctx)
	for _, version := range crd.Spec.Versions {
		objects, err := c.negotiatedApiResourceIndexer.ByIndex(clusterNameAndGVRIndexName, GetClusterNameAndGVRIndexKey(
			clusterName,
			metav1.GroupVersionResource{
				Group:    gvr.Group,
				Version:  version.Name,
				Resource: gvr.Resource,
			}))
		if err != nil {
			logger.Error(err, "error", "caller", runtime.GetCaller())
			return err
		}
		for _, obj := range objects {
			negotiatedAPIResource := obj.(*apiresourcev1alpha1.NegotiatedAPIResource).DeepCopy()
			negotiatedAPIResource.SetCondition(apiresourcev1alpha1.NegotiatedAPIResourceCondition{
				Type:   apiresourcev1alpha1.Enforced,
				Status: metav1.ConditionTrue,
			})
			if _, err := c.kcpClusterClient.Cluster(logicalcluster.From(negotiatedAPIResource)).ApiresourceV1alpha1().NegotiatedAPIResources().UpdateStatus(ctx, negotiatedAPIResource, metav1.UpdateOptions{}); err != nil {
				logger.Error(err, "error updating NegotiatedAPIResource status")
				return err
			}
			// TODO: manage the case when the manually applied CRD has no schema or an invalid schema...
			if err := negotiatedAPIResource.Spec.CommonAPIResourceSpec.SetSchema(version.Schema.OpenAPIV3Schema); err != nil {
				return err
			}
			if _, err := c.kcpClusterClient.Cluster(logicalcluster.From(negotiatedAPIResource)).ApiresourceV1alpha1().NegotiatedAPIResources().Update(ctx, negotiatedAPIResource, metav1.UpdateOptions{}); err != nil {
				logger.Error(err, "error updating NegotiatedAPIResource")
				return err
			}
		}
	}
	return nil
}

// setPublishingStatusOnNegotiatedAPIResource sets the status (Published / Refused) on the Negotiated API Resource for a CRD version
func (c *Controller) setPublishingStatusOnNegotiatedAPIResource(ctx context.Context, clusterName logicalcluster.Name, gvr metav1.GroupVersionResource, negotiatedAPIResource *apiresourcev1alpha1.NegotiatedAPIResource, crd *apiextensionsv1.CustomResourceDefinition) {
	if crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established) &&
		crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.NamesAccepted) &&
		!crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.NonStructuralSchema) &&
		!crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.Terminating) {
		negotiatedAPIResource.SetCondition(apiresourcev1alpha1.NegotiatedAPIResourceCondition{
			Type:   apiresourcev1alpha1.Published,
			Status: metav1.ConditionTrue,
		})
	} else if crdhelpers.IsCRDConditionFalse(crd, apiextensionsv1.Established) ||
		crdhelpers.IsCRDConditionFalse(crd, apiextensionsv1.NamesAccepted) ||
		crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.NonStructuralSchema) ||
		crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.Terminating) {
		negotiatedAPIResource.SetCondition(apiresourcev1alpha1.NegotiatedAPIResourceCondition{
			Type:   apiresourcev1alpha1.Published,
			Status: metav1.ConditionFalse,
		})
	}

	enforcedStatus := metav1.ConditionFalse
	if c.isManuallyCreatedCRD(ctx, crd) {
		enforcedStatus = metav1.ConditionTrue
	}
	negotiatedAPIResource.SetCondition(apiresourcev1alpha1.NegotiatedAPIResourceCondition{
		Type:   apiresourcev1alpha1.Enforced,
		Status: enforcedStatus,
	})
}

// updatePublishingStatusOnNegotiatedAPIResources sets the status (Published / Refused) on the Negotiated API Resource of each CRD version
func (c *Controller) updatePublishingStatusOnNegotiatedAPIResources(ctx context.Context, clusterName logicalcluster.Name, gvr metav1.GroupVersionResource, crd *apiextensionsv1.CustomResourceDefinition) error {
	logger := klog.FromContext(ctx)
	for _, version := range crd.Spec.Versions {
		objects, err := c.negotiatedApiResourceIndexer.ByIndex(clusterNameAndGVRIndexName, GetClusterNameAndGVRIndexKey(
			clusterName,
			metav1.GroupVersionResource{
				Group:    gvr.Group,
				Version:  version.Name,
				Resource: gvr.Resource,
			}))
		if err != nil {
			logger.Error(err, "error", "caller", runtime.GetCaller())
			return err
		}
		for _, obj := range objects {
			negotiatedAPIResource := obj.(*apiresourcev1alpha1.NegotiatedAPIResource).DeepCopy()
			c.setPublishingStatusOnNegotiatedAPIResource(ctx, clusterName, gvr, negotiatedAPIResource, crd)
			_, err := c.kcpClusterClient.Cluster(logicalcluster.From(negotiatedAPIResource)).ApiresourceV1alpha1().NegotiatedAPIResources().UpdateStatus(ctx, negotiatedAPIResource, metav1.UpdateOptions{})
			if err != nil {
				logger.Error(err, "error", "caller", runtime.GetCaller())
				return err
			}
		}
	}
	return nil
}

// deleteNegotiatedAPIResource deletes the Negotiated API Resource of each CRD version
// (they will be recreated from the related APIResourceImport objects if necessary,
// and if requested a CRD will be created again as a consequence).
func (c *Controller) deleteNegotiatedAPIResource(ctx context.Context, clusterName logicalcluster.Name, gvr metav1.GroupVersionResource, crd *apiextensionsv1.CustomResourceDefinition) error {
	logger := klog.FromContext(ctx)
	var gvrsToDelete []metav1.GroupVersionResource
	if gvr.Version != "" {
		gvrsToDelete = []metav1.GroupVersionResource{gvr}
	} else {
		if crd == nil {
			logger.Error(errors.New("CRD is nil after deletion"), "no way to find the NegotiatedAPIResources to delete from the CRD versions")
			return nil
		}
		for _, version := range crd.Spec.Versions {
			gvrsToDelete = append(gvrsToDelete, metav1.GroupVersionResource{
				Group:    gvr.Group,
				Version:  version.Name,
				Resource: gvr.Resource,
			})
		}
	}
	for _, gvrToDelete := range gvrsToDelete {
		objs, err := c.negotiatedApiResourceIndexer.ByIndex(clusterNameAndGVRIndexName, GetClusterNameAndGVRIndexKey(clusterName, gvrToDelete))
		if err != nil {
			logger.Error(err, "NegotiatedAPIResource for GVR could not be searched in index, and could not be deleted")
		}
		if len(objs) == 0 {
			logger.Info("NegotiatedAPIResource for GVR was not found and could not be deleted")
			continue
		}

		toDelete := objs[0].(*apiresourcev1alpha1.NegotiatedAPIResource)
		err = c.kcpClusterClient.Cluster(logicalcluster.From(toDelete)).ApiresourceV1alpha1().NegotiatedAPIResources().Delete(ctx, toDelete.Name, metav1.DeleteOptions{})
		if err != nil {
			logger.Error(err, "error", "caller", runtime.GetCaller())
			return err
		}
	}
	return nil
}

// ensureAPIResourceCompatibility ensures that the given APIResourceImport (or all imports related to the GVR if the import is nil)
// is compatible with the NegotiatedAPIResource. if possible and requested, it updates the NegotiatedAPIResource with the LCD of the
// schemas of the various imported schemas. If no NegotiatedAPIResource already exists, it can create one.
func (c *Controller) ensureAPIResourceCompatibility(ctx context.Context, clusterName logicalcluster.Name, gvr metav1.GroupVersionResource, apiResourceImport *apiresourcev1alpha1.APIResourceImport, overrideStrategy apiresourcev1alpha1.SchemaUpdateStrategyType) error {
	logger := klog.FromContext(ctx)
	// - if strategy allows schema update of the negotiated API resource (and current negotiated API resource is not enforced)
	// => Calculate the LCD of this APIResourceImport schema against the schema of the corresponding NegotiatedAPIResource. If not errors occur
	//    update the NegotiatedAPIResource schema. Update the current APIResourceImport status accordingly (possibly reporting errors).
	// - else (NegotiatedAPIResource schema update is not allowed)
	// => Just check the compatibility of this APIResourceImport schema against the schema of the corresponding NegotiatedAPIResource.
	//    Update the current APIResourceImport status accordingly (possibly reporting errors).

	var negotiatedAPIResource *apiresourcev1alpha1.NegotiatedAPIResource
	objs, err := c.negotiatedApiResourceIndexer.ByIndex(clusterNameAndGVRIndexName, GetClusterNameAndGVRIndexKey(clusterName, gvr))
	if err != nil {
		logger.Error(err, "error", "caller", runtime.GetCaller())
		return err
	}
	if len(objs) > 0 {
		negotiatedAPIResource = objs[0].(*apiresourcev1alpha1.NegotiatedAPIResource).DeepCopy()
	}

	var apiResourcesImports []*apiresourcev1alpha1.APIResourceImport
	if apiResourceImport != nil {
		apiResourcesImports = append(apiResourcesImports, apiResourceImport)
	} else {
		objs, err := c.apiResourceImportIndexer.ByIndex(clusterNameAndGVRIndexName, GetClusterNameAndGVRIndexKey(clusterName, gvr))
		if err != nil {
			logger.Error(err, "error", "caller", runtime.GetCaller())
			return err
		}
		for _, obj := range objs {
			apiResourcesImports = append(apiResourcesImports, obj.(*apiresourcev1alpha1.APIResourceImport).DeepCopy())
		}
	}

	if len(apiResourcesImports) == 0 {
		return nil
	}

	negotiatedAPIResourceName := gvr.Resource + "." + gvr.Version + "."
	if gvr.Group == "" {
		negotiatedAPIResourceName += "core"
	} else {
		negotiatedAPIResourceName += gvr.Group
	}

	var newNegotiatedAPIResource *apiresourcev1alpha1.NegotiatedAPIResource
	var updatedNegotiatedSchema bool
	if apiResourceImport != nil {
		// If a given apiResourceImport is given, then we are in the case of iterative partial comparison / LCD building
		// The final negotiated API resource will be based on the existing one.
		newNegotiatedAPIResource = negotiatedAPIResource
	}

	// If a corresponding manually added CRD exists,
	// then the final negotiated API resource will be based the one enforced from the CRD
	crdName := gvr.Resource + "."
	if gvr.Group == "" {
		crdName += "core"
	} else {
		crdName += gvr.Group
	}
	crd, err := c.crdLister.Cluster(clusterName).Get(crdName)
	if err != nil && !k8serrors.IsNotFound(err) {
		logger.Error(err, "error", "caller", runtime.GetCaller())
		return err
	}
	if crd != nil && c.isManuallyCreatedCRD(ctx, crd) {
		var crdVersion *apiextensionsv1.CustomResourceDefinitionVersion
		for i, version := range crd.Spec.Versions {
			if version.Name == gvr.Version {
				crdVersion = &crd.Spec.Versions[i]
			}
		}

		if crdVersion != nil {
			groupVersion := apiresourcev1alpha1.GroupVersion{
				Group:   gvr.Group,
				Version: gvr.Version,
			}
			newNegotiatedAPIResource = &apiresourcev1alpha1.NegotiatedAPIResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: negotiatedAPIResourceName,
					Annotations: map[string]string{
						logicalcluster.AnnotationKey:             clusterName.String(),
						apiresourcev1alpha1.APIVersionAnnotation: groupVersion.APIVersion(),
					},
				},
				Spec: apiresourcev1alpha1.NegotiatedAPIResourceSpec{
					CommonAPIResourceSpec: apiresourcev1alpha1.CommonAPIResourceSpec{
						GroupVersion:                  groupVersion,
						Scope:                         crd.Spec.Scope,
						CustomResourceDefinitionNames: crd.Spec.Names,
						SubResources:                  *(&apiresourcev1alpha1.SubResources{}).ImportFromCRDVersion(crdVersion),
						ColumnDefinitions:             *(&apiresourcev1alpha1.ColumnDefinitions{}).ImportFromCRDVersion(crdVersion),
					},
					Publish: true,
				},
			}
			if err := newNegotiatedAPIResource.Spec.SetSchema(crdVersion.Schema.OpenAPIV3Schema); err != nil {
				return err
			}
			newNegotiatedAPIResource.SetCondition(apiresourcev1alpha1.NegotiatedAPIResourceCondition{
				Type:   apiresourcev1alpha1.Published,
				Status: metav1.ConditionTrue,
			})
			newNegotiatedAPIResource.SetCondition(apiresourcev1alpha1.NegotiatedAPIResourceCondition{
				Type:   apiresourcev1alpha1.Enforced,
				Status: metav1.ConditionTrue,
			})
		}
	}

	var apiResourceImportUpdateStatusFuncs []func() error

	for i := range apiResourcesImports {
		apiResourceImport := apiResourcesImports[i].DeepCopy()

		if newNegotiatedAPIResource == nil {
			newNegotiatedAPIResource = &apiresourcev1alpha1.NegotiatedAPIResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: negotiatedAPIResourceName,
					Annotations: map[string]string{
						logicalcluster.AnnotationKey:             clusterName.String(),
						apiresourcev1alpha1.APIVersionAnnotation: apiResourceImport.Spec.CommonAPIResourceSpec.GroupVersion.APIVersion(),
					},
				},
				Spec: apiresourcev1alpha1.NegotiatedAPIResourceSpec{
					CommonAPIResourceSpec: apiResourceImport.Spec.CommonAPIResourceSpec,
					Publish:               c.AutoPublishNegotiatedAPIResource,
				},
			}
			if negotiatedAPIResource != nil {
				newNegotiatedAPIResource.ResourceVersion = negotiatedAPIResource.ResourceVersion
				newNegotiatedAPIResource.Spec.Publish = negotiatedAPIResource.Spec.Publish
			}
			updatedNegotiatedSchema = true
			apiResourceImport.SetCondition(apiresourcev1alpha1.APIResourceImportCondition{
				Type:    apiresourcev1alpha1.Compatible,
				Status:  metav1.ConditionTrue,
				Reason:  "",
				Message: "",
			})
			if value, found := apiResourceImport.Annotations[apiextensionsv1.KubeAPIApprovedAnnotation]; found {
				newNegotiatedAPIResource.Annotations[apiextensionsv1.KubeAPIApprovedAnnotation] = value
			}
		} else {
			allowUpdateNegotiatedSchema := !newNegotiatedAPIResource.IsConditionTrue(apiresourcev1alpha1.Enforced) &&
				apiResourceImport.Spec.SchemaUpdateStrategy.CanUpdate(newNegotiatedAPIResource.IsConditionTrue(apiresourcev1alpha1.Published))

			// TODO Also check compatibility of non-schema things like group, names, short names, category, resourcescope, subresources, columns etc...

			importSchema, err := apiResourceImport.Spec.GetSchema()
			if err != nil {
				logger.Error(err, "error", "caller", runtime.GetCaller())
				return err
			}
			negotiatedSchema, err := newNegotiatedAPIResource.Spec.GetSchema()
			if err != nil {
				logger.Error(err, "error", "caller", runtime.GetCaller())
				return err
			}

			apiResourceImport = apiResourceImport.DeepCopy()
			lcd, err := schemacompat.EnsureStructuralSchemaCompatibility(field.NewPath(newNegotiatedAPIResource.Spec.Kind), negotiatedSchema, importSchema, allowUpdateNegotiatedSchema)
			if err != nil {
				apiResourceImport.SetCondition(apiresourcev1alpha1.APIResourceImportCondition{
					Type:   apiresourcev1alpha1.Compatible,
					Status: metav1.ConditionFalse,
					Reason: "IncompatibleSchema",
					// TODO: improve error message.
					Message: err.Error(),
				})
			} else {
				apiResourceImport.SetCondition(apiresourcev1alpha1.APIResourceImportCondition{
					Type:    apiresourcev1alpha1.Compatible,
					Status:  metav1.ConditionTrue,
					Reason:  "",
					Message: "",
				})
				if newNegotiatedAPIResource.IsConditionTrue(apiresourcev1alpha1.Published) {
					apiResourceImport.SetCondition(apiresourcev1alpha1.APIResourceImportCondition{
						Type:    apiresourcev1alpha1.Available,
						Status:  metav1.ConditionTrue,
						Reason:  "",
						Message: "",
					})
				}
				if allowUpdateNegotiatedSchema {
					if err := newNegotiatedAPIResource.Spec.SetSchema(lcd); err != nil {
						return err
					}
					updatedNegotiatedSchema = true
				}
			}
		}
		apiResourceImportUpdateStatusFuncs = append(apiResourceImportUpdateStatusFuncs, func() error {
			lastOne, err := c.apiResourceImportLister.Cluster(logicalcluster.From(apiResourceImport)).Get(apiResourceImport.Name)
			if err != nil {
				logger.Error(err, "error", "caller", runtime.GetCaller())
				return err
			}
			apiResourceImport.SetResourceVersion(lastOne.GetResourceVersion())
			if _, err := c.kcpClusterClient.Cluster(logicalcluster.From(apiResourceImport)).ApiresourceV1alpha1().APIResourceImports().UpdateStatus(ctx, apiResourceImport, metav1.UpdateOptions{}); err != nil {
				logger.Error(err, "error", "caller", runtime.GetCaller())
				return err
			}
			return nil
		})
	}
	if negotiatedAPIResource == nil {
		existing, err := c.kcpClusterClient.Cluster(logicalcluster.From(newNegotiatedAPIResource)).ApiresourceV1alpha1().NegotiatedAPIResources().Create(ctx, newNegotiatedAPIResource, metav1.CreateOptions{})
		if k8serrors.IsAlreadyExists(err) {
			existing, err = c.kcpClusterClient.Cluster(logicalcluster.From(newNegotiatedAPIResource)).ApiresourceV1alpha1().NegotiatedAPIResources().Get(ctx, newNegotiatedAPIResource.Name, metav1.GetOptions{})
		}
		if err != nil {
			logger.Error(err, "error", "caller", runtime.GetCaller())
			return err
		}
		if len(newNegotiatedAPIResource.Status.Conditions) > 0 {
			existing.Status = newNegotiatedAPIResource.Status
			_, err = c.kcpClusterClient.Cluster(logicalcluster.From(existing)).ApiresourceV1alpha1().NegotiatedAPIResources().UpdateStatus(ctx, existing, metav1.UpdateOptions{})
			if err != nil {
				logger.Error(err, "error", "caller", runtime.GetCaller())
				return err
			}
		}
	} else if updatedNegotiatedSchema {
		if _, err := c.kcpClusterClient.Cluster(logicalcluster.From(newNegotiatedAPIResource)).ApiresourceV1alpha1().NegotiatedAPIResources().Update(ctx, newNegotiatedAPIResource, metav1.UpdateOptions{}); err != nil {
			logger.Error(err, "error", "caller", runtime.GetCaller())
			return err
		}
	}
	for _, apiResourceImportUpdateStatusFunc := range apiResourceImportUpdateStatusFuncs {
		if err := apiResourceImportUpdateStatusFunc(); err != nil {
			return err
		}
	}

	return nil
}

// negotiatedAPIResourceIsOrphan detects if there is no other APIResourceImport for this GVR and the current negotiated API resource is not enforced.
func (c *Controller) negotiatedAPIResourceIsOrphan(ctx context.Context, clusterName logicalcluster.Name, gvr metav1.GroupVersionResource) (bool, error) {
	logger := klog.FromContext(ctx)
	objs, err := c.apiResourceImportIndexer.ByIndex(clusterNameAndGVRIndexName, GetClusterNameAndGVRIndexKey(clusterName, gvr))
	if err != nil {
		logger.Error(err, "error", "caller", runtime.GetCaller())
		return false, err
	}

	if len(objs) > 0 {
		return false, nil
	}

	objs, err = c.negotiatedApiResourceIndexer.ByIndex(clusterNameAndGVRIndexName, GetClusterNameAndGVRIndexKey(clusterName, gvr))
	if err != nil {
		logger.Error(err, "error", "caller", runtime.GetCaller())
		return false, err
	}
	if len(objs) != 1 {
		return false, nil
	}
	negotiatedAPIResource := objs[0].(*apiresourcev1alpha1.NegotiatedAPIResource)
	return !negotiatedAPIResource.IsConditionTrue(apiresourcev1alpha1.Enforced), nil
}

// publishNegotiatedResource publishes the NegotiatedAPIResource information as a CRD, unless a manually-added CRD already exists for this GVR
func (c *Controller) publishNegotiatedResource(ctx context.Context, clusterName logicalcluster.Name, gvr metav1.GroupVersionResource, negotiatedApiResource *apiresourcev1alpha1.NegotiatedAPIResource) error {
	logger := klog.FromContext(ctx)
	crdName := gvr.Resource
	if gvr.Group == "" {
		crdName += ".core"
	} else {
		crdName += "." + gvr.Group
	}

	negotiatedSchema, err := negotiatedApiResource.Spec.CommonAPIResourceSpec.GetSchema()
	if err != nil {
		logger.Error(err, "error", "caller", runtime.GetCaller())
		return err
	}

	var subResources apiextensionsv1.CustomResourceSubresources
	for _, subResource := range negotiatedApiResource.Spec.SubResources {
		if subResource.Name == "scale" {
			subResources.Scale = &apiextensionsv1.CustomResourceSubresourceScale{
				SpecReplicasPath:   ".spec.replicas",
				StatusReplicasPath: ".status.replicas",
			}
		}
		if subResource.Name == "status" {
			subResources.Status = &apiextensionsv1.CustomResourceSubresourceStatus{}
		}
	}

	crColumnDefinitions := negotiatedApiResource.Spec.ColumnDefinitions.ToCustomResourceColumnDefinitions()

	crdVersion := apiextensionsv1.CustomResourceDefinitionVersion{
		Name:    gvr.Version,
		Storage: true, // TODO: How do we know which version will be stored ? the newest one we assume ?
		Served:  true, // TODO: Should we set served to false when the negotiated API is removed, instead of removing the CRD Version or CRD itself ?
		Schema: &apiextensionsv1.CustomResourceValidation{
			OpenAPIV3Schema: negotiatedSchema,
		},
		Subresources:             &subResources,
		AdditionalPrinterColumns: crColumnDefinitions,
	}

	crd, err := c.crdLister.Cluster(clusterName).Get(crdName)
	if err != nil && !k8serrors.IsNotFound(err) {
		logger.Error(err, "error", "caller", runtime.GetCaller())
		return err
	}

	//  If no CRD for the corresponding GR exists
	//  => create it with the right CRD version that corresponds to the NegotiatedAPIResource spec content (schema included)
	//     and add the current NegotiatedAPIResource as owner of the CRD
	if k8serrors.IsNotFound(err) {
		cr := &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name: crdName,
				OwnerReferences: []metav1.OwnerReference{
					NegotiatedAPIResourceAsOwnerReference(negotiatedApiResource),
				},
				// TODO: (shawn-hurley) We need to figure out how to set this
				Annotations: map[string]string{
					logicalcluster.AnnotationKey: logicalcluster.From(negotiatedApiResource).String(),
				},
			},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Scope: negotiatedApiResource.Spec.Scope,
				Group: negotiatedApiResource.Spec.GroupVersion.Group,
				Names: negotiatedApiResource.Spec.CustomResourceDefinitionNames,
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					crdVersion,
				},
			},
		}

		apiextensionsv1.SetDefaults_CustomResourceDefinition(cr)

		// In Kubernetes, to make it clear to the API consumer that APIs in *.k8s.io or *.kubernetes.io domains
		// should be following all quality standards of core Kubernetes, CRDs under these domains
		// are expected to go through the API Review process and so must link the API review approval PR
		// in an `api-approved.kubernetes.io` annotation.
		// Without this annotation, a CRD under the *.k8s.io or *.kubernetes.io domains is rejected by the API server
		//
		// Of course here we're simply adding already-known resources of existing physical clusters as CRDs in KCP.
		// But to please this Kubernetes approval requirement, let's add the required annotation in imported CRDs
		// with one of the KCP PRs that hacked Kubernetes CRD support for KCP.
		if crdhelpers.IsProtectedCommunityGroup(gvr.Group) {
			if cr.ObjectMeta.Annotations == nil {
				cr.ObjectMeta.Annotations = map[string]string{}
			}
			cr.ObjectMeta.Annotations["api-approved.kubernetes.io"] = "https://github.com/kcp-dev/kubernetes/pull/4"
		}

		if _, err := c.crdClusterClient.Cluster(clusterName).ApiextensionsV1().CustomResourceDefinitions().Create(ctx, cr, metav1.CreateOptions{}); err != nil {
			logger.Error(err, "error", "caller", runtime.GetCaller())
			return err
		}
	} else if !c.isManuallyCreatedCRD(ctx, crd) {
		//  If the CRD for the corresponding GVR exists and has a NegotiatedAPIResource owner
		//  => update the CRD version of the existing CRD with the NegotiatedAPIResource spec content (schema included),
		//     and add the current NegotiatedAPIResource as owner of the CRD

		crd = crd.DeepCopy()
		newCRDVersionIsTheLatest := true
		existingCRDVersionIndex := -1
		storageVersionIndex := -1
		for index, existingVersion := range crd.Spec.Versions {
			if existingVersion.Name == crdVersion.Name {
				existingCRDVersionIndex = index
			}
			if version.CompareKubeAwareVersionStrings(existingVersion.Name, crdVersion.Name) > 0 {
				newCRDVersionIsTheLatest = false
			}
			if existingVersion.Storage {
				storageVersionIndex = index
			}
		}

		if !newCRDVersionIsTheLatest {
			crdVersion.Storage = false
		} else if storageVersionIndex > -1 {
			crd.Spec.Versions[storageVersionIndex].Storage = false
		}

		if existingCRDVersionIndex == -1 {
			crd.Spec.Versions = append(crd.Spec.Versions, crdVersion)
		} else {
			crd.Spec.Versions[existingCRDVersionIndex] = crdVersion
		}

		var ownerReferenceAlreadyExists bool
		for _, ownerRef := range crd.OwnerReferences {
			if ownerRef.Name == negotiatedApiResource.Name && ownerRef.UID == negotiatedApiResource.UID {
				ownerReferenceAlreadyExists = true
				break
			}
		}

		if !ownerReferenceAlreadyExists {
			crd.OwnerReferences = append(crd.OwnerReferences,
				NegotiatedAPIResourceAsOwnerReference(negotiatedApiResource))
		}

		if _, err := c.crdClusterClient.Cluster(clusterName).ApiextensionsV1().CustomResourceDefinitions().Update(ctx, crd, metav1.UpdateOptions{}); err != nil {
			logger.Error(err, "error", "caller", runtime.GetCaller())
			return err
		}
	}

	// Update the NegotiatedAPIResource status to Submitted

	negotiatedApiResource = negotiatedApiResource.DeepCopy()
	negotiatedApiResource.SetCondition(apiresourcev1alpha1.NegotiatedAPIResourceCondition{
		Type:   apiresourcev1alpha1.Submitted,
		Status: metav1.ConditionTrue,
	})
	if _, err := c.kcpClusterClient.Cluster(logicalcluster.From(negotiatedApiResource)).ApiresourceV1alpha1().NegotiatedAPIResources().UpdateStatus(ctx, negotiatedApiResource, metav1.UpdateOptions{}); err != nil {
		logger.Error(err, "error", "caller", runtime.GetCaller())
		return err
	}

	return nil
}

// updateStatusOnRelatedAPIResourceImports udates the status of related compatible APIResourceImports, to set the `Available` condition to `true`
func (c *Controller) updateStatusOnRelatedAPIResourceImports(ctx context.Context, clusterName logicalcluster.Name, gvr metav1.GroupVersionResource, negotiatedApiResource *apiresourcev1alpha1.NegotiatedAPIResource) error {
	logger := klog.FromContext(ctx)
	publishedCondition := negotiatedApiResource.FindCondition(apiresourcev1alpha1.Published)
	if publishedCondition != nil {
		objs, err := c.apiResourceImportIndexer.ByIndex(clusterNameAndGVRIndexName, GetClusterNameAndGVRIndexKey(clusterName, gvr))
		if err != nil {
			logger.Error(err, "error", "caller", runtime.GetCaller())
			return err
		}
		for _, obj := range objs {
			apiResourceImport := obj.(*apiresourcev1alpha1.APIResourceImport).DeepCopy()
			apiResourceImport.SetCondition(apiresourcev1alpha1.APIResourceImportCondition{
				Type:   apiresourcev1alpha1.Available,
				Status: publishedCondition.Status,
			})
			if _, err := c.kcpClusterClient.Cluster(logicalcluster.From(apiResourceImport)).ApiresourceV1alpha1().APIResourceImports().UpdateStatus(ctx, apiResourceImport, metav1.UpdateOptions{}); err != nil {
				logger.Error(err, "error", "caller", runtime.GetCaller())
				return err
			}
		}
	}
	return nil
}

// cleanupNegotiatedAPIResource does the required cleanup of related resources (CRD,APIResourceImport) after a NegotiatedAPIResource has been deleted
func (c *Controller) cleanupNegotiatedAPIResource(ctx context.Context, clusterName logicalcluster.Name, gvr metav1.GroupVersionResource, negotiatedApiResource *apiresourcev1alpha1.NegotiatedAPIResource) error {
	logger := klog.FromContext(ctx)
	// In any case change the status on every APIResourceImport with the same GVR, to remove Compatible and Available conditions.

	objs, err := c.apiResourceImportIndexer.ByIndex(clusterNameAndGVRIndexName, GetClusterNameAndGVRIndexKey(clusterName, gvr))
	if err != nil {
		logger.Error(err, "error", "caller", runtime.GetCaller())
		return err
	}
	for _, obj := range objs {
		apiResourceImport := obj.(*apiresourcev1alpha1.APIResourceImport).DeepCopy()
		apiResourceImport.RemoveCondition(apiresourcev1alpha1.Available)
		apiResourceImport.RemoveCondition(apiresourcev1alpha1.Compatible)
		if _, err := c.kcpClusterClient.Cluster(logicalcluster.From(apiResourceImport)).ApiresourceV1alpha1().APIResourceImports().UpdateStatus(ctx, apiResourceImport, metav1.UpdateOptions{}); err != nil {
			logger.Error(err, "error", "caller", runtime.GetCaller())
			return err
		}
	}

	// if a CRD with the same GR has a version == to the current NegotiatedAPIResource version *and* has the current object as owner:
	// => if this CRD version is the only one, then delete the CRD
	//    else remove this CRD version from the CRD, as well as the corresponding owner

	crdName := gvr.Resource
	if gvr.Group == "" {
		crdName += ".core"
	} else {
		crdName += "." + gvr.Group
	}

	crd, err := c.crdLister.Cluster(clusterName).Get(crdName)
	if k8serrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		logger.Error(err, "error", "caller", runtime.GetCaller())
		return err
	}

	var ownerReferenceAlreadyExists bool
	var cleanedOwnerReferences []metav1.OwnerReference
	for _, ownerRef := range crd.OwnerReferences {
		if ownerRef.Name == negotiatedApiResource.Name && ownerRef.UID == negotiatedApiResource.UID {
			ownerReferenceAlreadyExists = true
			continue
		}
		cleanedOwnerReferences = append(cleanedOwnerReferences, ownerRef)
	}
	if !ownerReferenceAlreadyExists {
		return nil
	}

	var cleanedVersions []apiextensionsv1.CustomResourceDefinitionVersion
	for _, version := range crd.Spec.Versions {
		if version.Name == gvr.Version {
			continue
		}
		cleanedVersions = append(cleanedVersions, version)
	}
	if len(cleanedVersions) == len(crd.Spec.Versions) {
		return nil
	}
	if len(cleanedVersions) == 0 {
		if err := c.crdClusterClient.Cluster(clusterName).ApiextensionsV1().CustomResourceDefinitions().Delete(ctx, crd.Name, metav1.DeleteOptions{}); err != nil {
			logger.Error(err, "error", "caller", runtime.GetCaller())
			return err
		}
	} else {
		crd = crd.DeepCopy()
		crd.Spec.Versions = cleanedVersions
		crd.OwnerReferences = cleanedOwnerReferences
		if _, err := c.crdClusterClient.Cluster(clusterName).ApiextensionsV1().CustomResourceDefinitions().Update(ctx, crd, metav1.UpdateOptions{}); err != nil {
			logger.Error(err, "error", "caller", runtime.GetCaller())
			return err
		}
	}

	return nil
}
