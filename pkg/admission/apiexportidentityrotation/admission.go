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
//     earlier retirement (Manual -> After -> Immediate; After only shrinks),
//   - the After policy carries a positive duration,
//   - a rotation cannot be deleted while it is Migrating or AliasActive:
//     the controller retires the alias and clears the export's active-rotation
//     marker only from the rotation object, so deleting it mid-flight would
//     leave the old identity trusted forever.
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
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"

	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
)

const (
	PluginName = "migration.kcp.io/APIExportIdentityRotation"

	// rotationCooldown is the minimum interval between completed rotations
	// of the same export.
	rotationCooldown = time.Hour
)

// aliasRetirementRank orders the alias retirement policies from latest to
// earliest retirement. An update may only move a rotation toward earlier
// retirement, i.e. to a higher rank.
var aliasRetirementRank = map[migrationv1alpha1.AliasRetirementPolicy]int{
	migrationv1alpha1.AliasRetirementManual:    0,
	migrationv1alpha1.AliasRetirementAfter:     1,
	migrationv1alpha1.AliasRetirementImmediate: 2,
}

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &identityRotationAdmission{
				Handler: admission.NewHandler(admission.Create, admission.Update, admission.Delete),
				featureEnabled: func() bool {
					return kcpfeatures.DefaultFeatureGate.Enabled(kcpfeatures.APIExportIdentityRotation)
				},
			}, nil
		})
}

type identityRotationAdmission struct {
	*admission.Handler

	listRotations func(cluster logicalcluster.Name) ([]*migrationv1alpha1.APIExportIdentityRotation, error)
	// featureEnabled reports whether the APIExportIdentityRotation feature
	// gate is on; injectable so tests do not toggle the global gate.
	featureEnabled func() bool
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
	lister := local.Migration().V1alpha1().APIExportIdentityRotations().Lister()
	o.listRotations = func(cluster logicalcluster.Name) ([]*migrationv1alpha1.APIExportIdentityRotation, error) {
		return lister.Cluster(cluster).List(labels.Everything())
	}
}

func (o *identityRotationAdmission) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	if a.GetResource().GroupResource() != migrationv1alpha1.Resource("apiexportidentityrotations") {
		return nil
	}
	if a.GetSubresource() != "" {
		return nil
	}

	if a.GetOperation() == admission.Create && !o.featureEnabled() {
		// without the gate no controller acts on the object; refuse it
		// instead of leaving a rotation that never progresses.
		return admission.NewForbidden(a, fmt.Errorf("the %s feature gate is disabled", kcpfeatures.APIExportIdentityRotation))
	}

	if a.GetOperation() == admission.Delete {
		// on delete the object being removed is the old object.
		old, err := toRotation(a.GetOldObject())
		if err != nil {
			return err
		}
		return validateDelete(a, old)
	}

	rotation, err := toRotation(a.GetObject())
	if err != nil {
		return err
	}
	if err := validateSpec(a, rotation); err != nil {
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

// validateSpec checks invariants of the spec on its own, regardless of the
// operation.
func validateSpec(a admission.Attributes, rotation *migrationv1alpha1.APIExportIdentityRotation) error {
	if rotation.Spec.AliasRetirement.Policy == migrationv1alpha1.AliasRetirementAfter {
		after := rotation.Spec.AliasRetirement.After
		if after == nil || after.Duration <= 0 {
			return admission.NewForbidden(a, fmt.Errorf("spec.aliasRetirement.after must be a positive duration for the %s policy", migrationv1alpha1.AliasRetirementAfter))
		}
	}
	return nil
}

// validateDelete rejects deleting a rotation that has flipped the export and
// not yet retired the alias. Everything the controller still has to undo on
// the export (alias, active-rotation marker) is driven from the rotation
// object; without it the old identity would stay trusted forever.
func validateDelete(a admission.Attributes, rotation *migrationv1alpha1.APIExportIdentityRotation) error {
	switch rotation.Status.Phase {
	case migrationv1alpha1.APIExportIdentityRotationMigrating, migrationv1alpha1.APIExportIdentityRotationAliasActive:
		return admission.NewForbidden(a, fmt.Errorf("rotation %q is %s; move spec.aliasRetirement.policy to Immediate and wait for it to complete before deleting it",
			rotation.Name, rotation.Status.Phase))
	}
	return nil
}

func (o *identityRotationAdmission) validateCreate(clusterName logicalcluster.Name, a admission.Attributes, rotation *migrationv1alpha1.APIExportIdentityRotation) error {
	existing, err := o.listRotations(clusterName)
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
			if latestCompleted == nil || completionTime(r).After(completionTime(latestCompleted)) {
				latestCompleted = r
			}
		case migrationv1alpha1.APIExportIdentityRotationFailed:
		default:
			return admission.NewForbidden(a, fmt.Errorf("rotation %q for APIExport %q is still active (%s); at most one rotation per export may be active",
				r.Name, r.Spec.Export.Name, r.Status.Phase))
		}
	}

	if latestCompleted != nil {
		if since := time.Since(completionTime(latestCompleted)); since < rotationCooldown {
			return admission.NewForbidden(a, fmt.Errorf("APIExport %q was rotated %s ago by %q; rotations of the same export require a %s cooldown",
				rotation.Spec.Export.Name, since.Round(time.Second), latestCompleted.Name, rotationCooldown))
		}
	}

	return nil
}

// completionTime is when a Completed rotation retired its alias: the
// transition time of the AliasRetired condition. The cooldown counts from
// there, not from creation, so a long drain does not eat into it. Objects
// without the condition (none are written by this controller) fall back to
// their creation time.
func completionTime(rotation *migrationv1alpha1.APIExportIdentityRotation) time.Time {
	if cond := conditions.Get(rotation, migrationv1alpha1.IdentityRotationAliasRetired); cond != nil && !cond.LastTransitionTime.IsZero() {
		return cond.LastTransitionTime.Time
	}
	return rotation.CreationTimestamp.Time
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
	if aliasRetirementRank[newPolicy] < aliasRetirementRank[oldPolicy] {
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
