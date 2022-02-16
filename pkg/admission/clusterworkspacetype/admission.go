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

package clusterworkspacetype

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"

	kcpadmissionhelpers "github.com/kcp-dev/kcp/pkg/admission/helpers"
	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	tenancyv1alpha1lister "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
)

// Validate ClusterWorkspace creation and updates for valid use of spec.type, i.e. the
// ClusterWorkspaceType must exist in the same workspace and the field is immutable after
// creation.

const (
	PluginName = "tenancy.kcp.dev/ClusterWorkspaceType"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &clusterworkspacetypeExists{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}, nil
		})
}

type clusterworkspacetypeExists struct {
	*admission.Handler
	typeLister    tenancyv1alpha1lister.ClusterWorkspaceTypeLister
	cachesToSync  []cache.InformerSynced
	cacheSyncLock cacheSync
}

// Ensure that the required admission interfaces are implemented.
var _ = admission.ValidationInterface(&clusterworkspacetypeExists{})
var _ = kcpinitializers.WantsKcpInformers(&clusterworkspacetypeExists{})
var _ = admission.MutationInterface(&clusterworkspacetypeExists{})
var _ = admission.ValidationInterface(&clusterworkspacetypeExists{})

func (o *clusterworkspacetypeExists) Admit(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	if a.GetResource().GroupResource() != tenancyv1alpha1.Resource("clusterworkspaces") {
		return nil
	}
	if a.GetOperation() != admission.Update {
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return nil
	}
	obj, err := kcpadmissionhelpers.DecodeUnstructured(u)
	if err != nil {
		// nolint: nilerr
		return nil // only work on unstructured ClusterWorkspaces
	}
	cw, ok := obj.(*tenancyv1alpha1.ClusterWorkspace)
	if !ok {
		// nolint: nilerr
		return nil // only work on unstructured ClusterWorkspaces
	}

	obj, err = kcpadmissionhelpers.NativeObject(a.GetOldObject())
	if err != nil {
		return fmt.Errorf("unexpected unknown old object, got %v, expected CLusterWorkspace", a.GetOldObject().GetObjectKind().GroupVersionKind().Kind)
	}
	old, ok := obj.(*tenancyv1alpha1.ClusterWorkspace)
	if !ok {
		return fmt.Errorf("unexpected unknown old object, got %v, expected ClusterWorkspace", obj.GetObjectKind().GroupVersionKind().Kind)
	}

	// we only admit at state transition to initializing
	transitioningToInitializing :=
		old.Status.Phase != tenancyv1alpha1.ClusterWorkspacePhaseInitializing &&
			cw.Status.Phase == tenancyv1alpha1.ClusterWorkspacePhaseInitializing
	if !transitioningToInitializing {
		return nil
	}

	if !o.waitForSyncedStore(ctx) {
		return admission.NewForbidden(a, errors.New(PluginName+": caches not synchronized"))
	}

	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	cwt, err := o.typeLister.Get(clusters.ToClusterAwareKey(clusterName, strings.ToLower(cw.Spec.Type)))
	if err != nil && apierrors.IsNotFound(err) {
		if cw.Spec.Type == "Universal" {
			return nil // Universal is always valid
		}
		return admission.NewForbidden(a, fmt.Errorf("spec.type %q does not exist", cw.Spec.Type))
	} else if err != nil {
		return admission.NewForbidden(a, err)
	}

	// add initializers from type to workspace
	existing := sets.NewString()
	for _, i := range cw.Status.Initializers {
		existing.Insert(string(i))
	}
	for _, i := range cwt.Spec.Initializers {
		if !existing.Has(string(i)) {
			cw.Status.Initializers = append(cw.Status.Initializers, i)
		}
	}

	if err := kcpadmissionhelpers.EncodeIntoUnstructured(u, cw); err != nil {
		return err
	}

	return nil
}

// Validate ensures that routes specify required annotations, and returns nil if valid.
// The admission handler ensures this is only called for Create/Update operations.
func (o *clusterworkspacetypeExists) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	if a.GetResource().GroupResource() != tenancyv1alpha1.Resource("clusterworkspaces") {
		return nil
	}

	obj, err := kcpadmissionhelpers.NativeObject(a.GetObject())
	if err != nil {
		// nolint: nilerr
		return nil // only work on unstructured ClusterWorkspaces
	}
	cw, ok := obj.(*tenancyv1alpha1.ClusterWorkspace)
	if !ok {
		// nolint: nilerr
		return nil // only work on unstructured ClusterWorkspaces
	}

	var old *tenancyv1alpha1.ClusterWorkspace
	var transitioningToInitializing bool
	if a.GetOperation() == admission.Update {
		obj, err = kcpadmissionhelpers.NativeObject(a.GetOldObject())
		if err != nil {
			return fmt.Errorf("unexpected unknown old object, got %v, expected CLusterWorkspace", a.GetOldObject().GetObjectKind().GroupVersionKind().Kind)
		}
		old, ok = obj.(*tenancyv1alpha1.ClusterWorkspace)
		if !ok {
			return fmt.Errorf("unexpected unknown old object, got %v, expected ClusterWorkspace", obj.GetObjectKind().GroupVersionKind().Kind)
		}

		transitioningToInitializing = old.Status.Phase != tenancyv1alpha1.ClusterWorkspacePhaseInitializing &&
			cw.Status.Phase == tenancyv1alpha1.ClusterWorkspacePhaseInitializing
	}

	if !o.waitForSyncedStore(ctx) {
		return admission.NewForbidden(a, errors.New(PluginName+": caches not synchronized"))
	}

	// check type on create and on state transition
	// TODO(sttts): there is a race that the type can be deleted between scheduling and initializing
	//              but we cannot add initializers in status on create. A controller doing that wouldn't fix
	//		        the race either. So, ¯\_(ツ)_/¯. Chance is low. Object can be deleted, or a condition could should
	//              show it failing.
	if (a.GetOperation() == admission.Update && transitioningToInitializing) || a.GetOperation() == admission.Create {
		clusterName, err := genericapirequest.ClusterNameFrom(ctx)
		if err != nil {
			return apierrors.NewInternalError(err)
		}

		cwt, err := o.typeLister.Get(clusters.ToClusterAwareKey(clusterName, strings.ToLower(cw.Spec.Type)))
		if err != nil && apierrors.IsNotFound(err) {
			if cw.Spec.Type == "Universal" {
				return nil // Universal is always valid
			}
			return admission.NewForbidden(a, fmt.Errorf("spec.type %q does not exist", cw.Spec.Type))
		} else if err != nil {
			return admission.NewForbidden(a, err)
		}

		if a.GetOperation() == admission.Update {
			// this is a trnasition to initializing. Check that all initializers are there
			// (no other admission plugin removed any).
			existing := sets.NewString()
			for _, i := range cw.Status.Initializers {
				existing.Insert(string(i))
			}
			for _, i := range cwt.Spec.Initializers {
				if !existing.Has(string(i)) {
					return admission.NewForbidden(a, fmt.Errorf("spec.initializers %q does not exist", i))
				}
			}
		}
	}

	// Determine if there are HSTS changes in this update
	if a.GetOperation() == admission.Update {
		if old.Spec.Type != cw.Spec.Type {
			return admission.NewForbidden(a, errors.New("spec.type is immutable"))
		}

		if transitioningToInitializing {
			return nil
		}
	}

	// TODO: add SubjectAccessReview against clusterworkspacetypes/use.

	return nil
}

// waitForSyncedStore calls cache.WaitForCacheSync, which will wait up to timeToWaitForCacheSync
// for the cachesToSync to synchronize.
func (o *clusterworkspacetypeExists) waitForSyncedStore(ctx context.Context) bool {
	syncCtx, cancelFn := context.WithTimeout(ctx, 30*time.Second)
	defer cancelFn()
	if !o.cacheSyncLock.hasSynced() {
		if !cache.WaitForCacheSync(syncCtx.Done(), o.cachesToSync...) {
			return false
		}
		o.cacheSyncLock.setSynced()
	}
	return true
}

func (o *clusterworkspacetypeExists) ValidateInitialization() error {
	if o.typeLister == nil {
		return fmt.Errorf(PluginName + " plugin needs an ClusterWorkspaceType lister")
	}
	if len(o.cachesToSync) < 1 {
		return fmt.Errorf(PluginName + " plugin missing informer synced functions")
	}
	return nil
}

func (o *clusterworkspacetypeExists) SetKcpInformers(informers kcpinformers.SharedInformerFactory) {
	o.cachesToSync = append(o.cachesToSync, informers.Tenancy().V1alpha1().ClusterWorkspaceTypes().Informer().HasSynced)
	o.typeLister = informers.Tenancy().V1alpha1().ClusterWorkspaceTypes().Lister()
}

// cacheSync guards the isSynced variable
// Once isSynced is true, we don't care about setting it anymore
type cacheSync struct {
	isSyncedLock sync.RWMutex
	isSynced     bool
}

func (cs *cacheSync) hasSynced() bool {
	cs.isSyncedLock.RLock()
	defer cs.isSyncedLock.RUnlock()
	return cs.isSynced
}
func (cs *cacheSync) setSynced() {
	cs.isSyncedLock.Lock()
	defer cs.isSyncedLock.Unlock()
	cs.isSynced = true
}
