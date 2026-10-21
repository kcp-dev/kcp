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

// Package apiexportstoragerekey enforces the safety invariants of APIExport
// storage re-keying (enhancements KEP 0008):
//
//   - at most one active re-keying per export,
//   - a minimum interval between completed re-keyings of the same export
//     (bounds even a delegated-but-compromised provider's ability to loop
//     re-keyings into sustained consumer fencing),
//   - spec immutability,
//   - a re-keying cannot be deleted while it is Draining: the controller
//     clears the export's active-rekey marker only from the re-keying
//     object, so deleting it mid-flight would leave the drain orphaned.
package apiexportstoragerekey

import (
	"context"
	"fmt"
	"io"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
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
	PluginName = "migration.kcp.io/APIExportStorageRekey"

	// rekeyCooldown is the minimum interval between completed re-keyings of
	// the same export.
	rekeyCooldown = time.Hour
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &storageRekeyAdmission{
				Handler: admission.NewHandler(admission.Create, admission.Update, admission.Delete),
				featureEnabled: func() bool {
					return kcpfeatures.DefaultFeatureGate.Enabled(kcpfeatures.APIExportStorageRekey)
				},
			}, nil
		})
}

type storageRekeyAdmission struct {
	*admission.Handler

	listRekeys func(cluster logicalcluster.Name) ([]*migrationv1alpha1.APIExportStorageRekey, error)
	// featureEnabled reports whether the APIExportStorageRekey feature gate
	// is on; injectable so tests do not toggle the global gate.
	featureEnabled func() bool
}

// Ensure that the required admission interfaces are implemented.
var (
	_ = admission.ValidationInterface(&storageRekeyAdmission{})
	_ = kcpinitializers.WantsKcpInformers(&storageRekeyAdmission{})
)

func (o *storageRekeyAdmission) SetKcpInformers(local, _ kcpinformers.SharedInformerFactory) {
	rekeysReady := local.Migration().V1alpha1().APIExportStorageRekeys().Informer().HasSynced
	o.SetReadyFunc(func() bool {
		return rekeysReady()
	})
	lister := local.Migration().V1alpha1().APIExportStorageRekeys().Lister()
	o.listRekeys = func(cluster logicalcluster.Name) ([]*migrationv1alpha1.APIExportStorageRekey, error) {
		return lister.Cluster(cluster).List(labels.Everything())
	}
}

func (o *storageRekeyAdmission) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	if a.GetResource().GroupResource() != migrationv1alpha1.Resource("apiexportstoragerekeys") {
		return nil
	}
	if a.GetSubresource() != "" {
		return nil
	}

	if a.GetOperation() == admission.Create && !o.featureEnabled() {
		// without the gate no controller acts on the object; refuse it
		// instead of leaving a re-keying that never progresses.
		return admission.NewForbidden(a, fmt.Errorf("the %s feature gate is disabled", kcpfeatures.APIExportStorageRekey))
	}

	if a.GetOperation() == admission.Delete {
		// on delete the object being removed is the old object.
		old, err := toRekey(a.GetOldObject())
		if err != nil {
			return err
		}
		return validateDelete(a, old)
	}

	rekey, err := toRekey(a.GetObject())
	if err != nil {
		return err
	}

	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return errors.NewInternalError(err)
	}

	switch a.GetOperation() {
	case admission.Create:
		return o.validateCreate(clusterName, a, rekey)
	case admission.Update:
		old, err := toRekey(a.GetOldObject())
		if err != nil {
			return err
		}
		return validateUpdate(a, old, rekey)
	}
	return nil
}

// validateDelete rejects deleting a re-keying that has marked the export and
// not yet finished draining. Everything the controller still has to undo on
// the export (the active-rekey marker) is driven from the re-keying object;
// without it the drain would be orphaned.
func validateDelete(a admission.Attributes, rekey *migrationv1alpha1.APIExportStorageRekey) error {
	if rekey.Status.Phase == migrationv1alpha1.APIExportStorageRekeyDraining {
		return admission.NewForbidden(a, fmt.Errorf("re-keying %q is %s; wait for it to complete before deleting it",
			rekey.Name, rekey.Status.Phase))
	}
	return nil
}

func (o *storageRekeyAdmission) validateCreate(clusterName logicalcluster.Name, a admission.Attributes, rekey *migrationv1alpha1.APIExportStorageRekey) error {
	existing, err := o.listRekeys(clusterName)
	if err != nil {
		return admission.NewForbidden(a, fmt.Errorf("failed to list existing re-keyings: %w", err))
	}
	var latestCompleted *migrationv1alpha1.APIExportStorageRekey
	for _, r := range existing {
		if r.Spec.Export != rekey.Spec.Export {
			continue
		}
		switch r.Status.Phase {
		case migrationv1alpha1.APIExportStorageRekeyCompleted:
			if latestCompleted == nil || completionTime(r).After(completionTime(latestCompleted)) {
				latestCompleted = r
			}
		case migrationv1alpha1.APIExportStorageRekeyFailed:
			// terminal and harmless; it never marked the export.
		default:
			return admission.NewForbidden(a, fmt.Errorf("re-keying %q of APIExport %q is still active (%s); at most one re-keying per export may be active",
				r.Name, r.Spec.Export.Name, phaseOrPending(r.Status.Phase)))
		}
	}
	if latestCompleted != nil {
		if since := time.Since(completionTime(latestCompleted)); since < rekeyCooldown {
			return admission.NewForbidden(a, fmt.Errorf("APIExport %q was re-keyed %s ago by %q; re-keying is in cooldown for another %s",
				rekey.Spec.Export.Name, since.Round(time.Second), latestCompleted.Name, (rekeyCooldown-since).Round(time.Second)))
		}
	}
	return nil
}

// completionTime is when a completed re-keying finished its drain, falling
// back to its creation time for objects without the condition.
func completionTime(r *migrationv1alpha1.APIExportStorageRekey) time.Time {
	if cond := conditions.Get(r, migrationv1alpha1.StorageRekeyDrained); cond != nil && !cond.LastTransitionTime.IsZero() {
		return cond.LastTransitionTime.Time
	}
	return r.CreationTimestamp.Time
}

func phaseOrPending(phase migrationv1alpha1.APIExportStorageRekeyPhase) migrationv1alpha1.APIExportStorageRekeyPhase {
	if phase == "" {
		return migrationv1alpha1.APIExportStorageRekeyPending
	}
	return phase
}

// validateUpdate keeps the spec immutable. The CEL rule on spec.export
// covers the API-server path; this also covers the resources and target key
// and any client that bypasses CEL.
func validateUpdate(a admission.Attributes, old, rekey *migrationv1alpha1.APIExportStorageRekey) error {
	if !equality.Semantic.DeepEqual(old.Spec, rekey.Spec) {
		return admission.NewForbidden(a, fmt.Errorf("spec is immutable"))
	}
	return nil
}

func toRekey(obj runtime.Object) (*migrationv1alpha1.APIExportStorageRekey, error) {
	switch o := obj.(type) {
	case *migrationv1alpha1.APIExportStorageRekey:
		return o, nil
	case *unstructured.Unstructured:
		rekey := &migrationv1alpha1.APIExportStorageRekey{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, rekey); err != nil {
			return nil, fmt.Errorf("failed to convert unstructured to APIExportStorageRekey: %w", err)
		}
		return rekey, nil
	default:
		return nil, fmt.Errorf("unexpected type %T", obj)
	}
}
