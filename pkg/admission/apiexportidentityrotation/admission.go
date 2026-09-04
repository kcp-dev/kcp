/*
Copyright 2026 The kcp Authors.

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

// Package apiexportidentityrotation enforces the safety invariants of
// APIExport identity rotation (enhancements KEP 0005):
//
//   - at most one active rotation per export,
//   - a minimum interval between completed rotations of the same export
//     (bounds even a delegated-but-compromised provider's ability to loop
//     rotations into sustained consumer fencing),
//   - spec immutability except aliasRetirement, which may only move toward
//     earlier retirement (Manual -> After -> Immediate; After only shrinks).
package apiexportidentityrotation

import (
	"context"
	"fmt"
	"io"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
	migrationv1alpha1listers "github.com/kcp-dev/sdk/client/listers/migration/v1alpha1"

	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
)

const (
	PluginName = "migration.kcp.io/APIExportIdentityRotation"

	// rotationCooldown is the minimum interval between completed rotations
	// of the same export.
	rotationCooldown = time.Hour
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &identityRotationAdmission{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}, nil
		})
}

type identityRotationAdmission struct {
	*admission.Handler

	rotationLister migrationv1alpha1listers.APIExportIdentityRotationClusterLister
}

// Ensure that the required admission interfaces are implemented.
var (
	_ = admission.ValidationInterface(&identityRotationAdmission{})
	_ = kcpinitializers.WantsKcpInformers(&identityRotationAdmission{})
)

func (o *identityRotationAdmission) SetKcpInformers(local, _ kcpinformers.SharedInformerFactory) {
	rotationsReady := local.Migration().V1alpha1().APIExportIdentityRotations().Informer().HasSynced
	o.SetReadyFunc(func() bool {
		return rotationsReady()
	})
	o.rotationLister = local.Migration().V1alpha1().APIExportIdentityRotations().Lister()
}

func (o *identityRotationAdmission) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	if a.GetResource().GroupResource() != migrationv1alpha1.Resource("apiexportidentityrotations") {
		return nil
	}
	if a.GetSubresource() != "" {
		return nil
	}

	rotation, err := toRotation(a.GetObject())
	if err != nil {
		return err
	}

	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return errors.NewInternalError(err)
	}

	switch a.GetOperation() {
	case admission.Create:
		return o.validateCreate(clusterName, a, rotation)
	case admission.Update:
		old, err := toRotation(a.GetOldObject())
		if err != nil {
			return err
		}
		return validateUpdate(a, old, rotation)
	}
	return nil
}

func (o *identityRotationAdmission) validateCreate(clusterName logicalcluster.Name, a admission.Attributes, rotation *migrationv1alpha1.APIExportIdentityRotation) error {
	existing, err := o.rotationLister.Cluster(clusterName).List(labels.Everything())
	if err != nil {
		return admission.NewForbidden(a, fmt.Errorf("failed to list existing rotations: %w", err))
	}

	var latestCompleted *migrationv1alpha1.APIExportIdentityRotation
	for _, r := range existing {
		if r.Spec.Export != rotation.Spec.Export {
			continue
		}
		switch r.Status.Phase {
		case migrationv1alpha1.APIExportIdentityRotationCompleted:
			if latestCompleted == nil || r.CreationTimestamp.After(latestCompleted.CreationTimestamp.Time) {
				latestCompleted = r
			}
		case migrationv1alpha1.APIExportIdentityRotationFailed:
		default:
			return admission.NewForbidden(a, fmt.Errorf("rotation %q for APIExport %q is still active (%s); at most one rotation per export may be active",
				r.Name, r.Spec.Export.Name, r.Status.Phase))
		}
	}

	if latestCompleted != nil {
		if since := time.Since(latestCompleted.CreationTimestamp.Time); since < rotationCooldown {
			return admission.NewForbidden(a, fmt.Errorf("APIExport %q was rotated %s ago by %q; rotations of the same export require a %s cooldown",
				rotation.Spec.Export.Name, since.Round(time.Second), latestCompleted.Name, rotationCooldown))
		}
	}

	return nil
}

func validateUpdate(a admission.Attributes, old, rotation *migrationv1alpha1.APIExportIdentityRotation) error {
	if rotation.Spec.Export != old.Spec.Export {
		return admission.NewForbidden(a, fmt.Errorf("spec.export is immutable"))
	}
	oldRef, newRef := old.Spec.NewIdentity.SecretRef, rotation.Spec.NewIdentity.SecretRef
	if (oldRef == nil) != (newRef == nil) || (oldRef != nil && *oldRef != *newRef) {
		return admission.NewForbidden(a, fmt.Errorf("spec.newIdentity is immutable"))
	}

	oldPolicy := old.Spec.AliasRetirement.Policy
	newPolicy := rotation.Spec.AliasRetirement.Policy
	if oldPolicy == "" {
		oldPolicy = migrationv1alpha1.AliasRetirementManual
	}
	if newPolicy == "" {
		newPolicy = migrationv1alpha1.AliasRetirementManual
	}
	rank := map[migrationv1alpha1.AliasRetirementPolicy]int{
		migrationv1alpha1.AliasRetirementManual:    0,
		migrationv1alpha1.AliasRetirementAfter:     1,
		migrationv1alpha1.AliasRetirementImmediate: 2,
	}
	if rank[newPolicy] < rank[oldPolicy] {
		return admission.NewForbidden(a, fmt.Errorf("spec.aliasRetirement may only move toward earlier retirement (Manual -> After -> Immediate)"))
	}
	if oldPolicy == migrationv1alpha1.AliasRetirementAfter && newPolicy == migrationv1alpha1.AliasRetirementAfter {
		if old.Spec.AliasRetirement.After != nil && rotation.Spec.AliasRetirement.After != nil &&
			rotation.Spec.AliasRetirement.After.Duration > old.Spec.AliasRetirement.After.Duration {
			return admission.NewForbidden(a, fmt.Errorf("spec.aliasRetirement.after may only shrink"))
		}
	}
	return nil
}

func toRotation(obj runtime.Object) (*migrationv1alpha1.APIExportIdentityRotation, error) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		if r, ok := obj.(*migrationv1alpha1.APIExportIdentityRotation); ok {
			return r, nil
		}
		return nil, fmt.Errorf("unexpected type %T", obj)
	}
	rotation := &migrationv1alpha1.APIExportIdentityRotation{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, rotation); err != nil {
		return nil, fmt.Errorf("failed to convert unstructured to APIExportIdentityRotation: %w", err)
	}
	return rotation, nil
}
