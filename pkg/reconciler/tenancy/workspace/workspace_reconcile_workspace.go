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

package workspace

import (
	"context"
	"reflect"

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/projection"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
)

const (
	// mirrorWorkspaceFinalizer is put on the ClusterWorkspace to block deletion until the Workspace is deleted.
	mirrorWorkspaceFinalizer = "internal.tenancy.kcp.dev/mirror-workspace"
)

// workspaceReconciler is a temporary reconciler that creates real Workspace objects
// by-passing the projection to ClusterWorkspaces.
type workspaceReconciler struct {
	getWorkspace                           func(clusterName logicalcluster.Name, name string) (*tenancyv1beta1.Workspace, error)
	deleteWorkspaceWithoutProjection       func(ctx context.Context, clusterName logicalcluster.Name, name string) error
	createWorkspaceWithoutProjection       func(ctx context.Context, clusterName logicalcluster.Name, this *tenancyv1beta1.Workspace) (*tenancyv1beta1.Workspace, error)
	updateWorkspaceWithoutProjection       func(ctx context.Context, clusterName logicalcluster.Name, this *tenancyv1beta1.Workspace) (*tenancyv1beta1.Workspace, error)
	updateWorkspaceStatusWithoutProjection func(ctx context.Context, clusterName logicalcluster.Name, this *tenancyv1beta1.Workspace) (*tenancyv1beta1.Workspace, error)
}

func (r *workspaceReconciler) reconcile(ctx context.Context, cw *tenancyv1alpha1.ClusterWorkspace) (reconcileStatus, error) {
	logger := klog.FromContext(ctx).WithValues("reconciler", "workspace")

	// not about new workspaces
	if cw.Status.Phase == tenancyv1alpha1.WorkspacePhaseScheduling {
		return reconcileStatusContinue, nil
	}

	// mirror deletion timestamp to Workspace
	if !cw.DeletionTimestamp.IsZero() {
		ws, err := r.getWorkspace(logicalcluster.From(cw), cw.Name)
		if err != nil && !errors.IsNotFound(err) {
			return reconcileStatusStopAndRequeue, err
		} else if err == nil && ws.DeletionTimestamp.IsZero() {
			logger.Info("Deleting Workspace from ClusterWorkspace")
			if err = r.deleteWorkspaceWithoutProjection(ctx, logicalcluster.From(cw), cw.Name); err != nil && !errors.IsNotFound(err) {
				return reconcileStatusStopAndRequeue, err
			}
		}

		// Workspace is gone. Remove finalizer from ClusterWorkspace
		cw.Finalizers = sets.NewString(cw.Finalizers...).Delete(mirrorWorkspaceFinalizer).List()

		return reconcileStatusContinue, nil
	}

	// get and possibly create Workspace
	expectedFinalizers := sets.NewString(cw.Finalizers...).Delete(mirrorWorkspaceFinalizer)
	ws, err := r.getWorkspace(logicalcluster.From(cw), cw.Name)
	if err != nil && !errors.IsNotFound(err) {
		return reconcileStatusStopAndRequeue, nil
	} else if errors.IsNotFound(err) {
		ws := &tenancyv1beta1.Workspace{}
		projection.ProjectClusterWorkspaceToWorkspace(cw, ws)
		ws.ObjectMeta = metav1.ObjectMeta{
			Name:        cw.Name,
			Annotations: ws.Annotations,
			Labels:      ws.Labels,
			Finalizers:  expectedFinalizers.List(),
		}
		logger.Info("Creating Workspace from ClusterWorkspace")
		if _, err = r.createWorkspaceWithoutProjection(ctx, logicalcluster.From(cw), ws); err != nil && !errors.IsAlreadyExists(err) {
			return reconcileStatusStopAndRequeue, err
		} else if errors.IsAlreadyExists(err) {
			ws, err = r.getWorkspace(logicalcluster.From(cw), cw.Name)
			if err != nil {
				return reconcileStatusStopAndRequeue, err
			}
		}
	}

	// update metadata and spec of Workspace
	if !reflect.DeepEqual(ws.Annotations, cw.Annotations) ||
		!reflect.DeepEqual(ws.Labels, cw.Labels) ||
		!sets.NewString(ws.Finalizers...).Equal(expectedFinalizers) {
		ws = ws.DeepCopy()
		ws.Annotations = cw.Annotations
		ws.Labels = cw.Labels
		ws.Finalizers = expectedFinalizers.List()
		logger.Info("Updating Workspace from ClusterWorkspace", "reason", "labels/annotations")
		if ws, err = r.updateWorkspaceWithoutProjection(ctx, logicalcluster.From(cw), ws); err != nil {
			return reconcileStatusStopAndRequeue, err
		}
	}

	// update status of Workspace
	var updated tenancyv1beta1.Workspace
	projection.ProjectClusterWorkspaceToWorkspace(cw, &updated)
	if !reflect.DeepEqual(ws.Status, updated.Status) {
		ws = ws.DeepCopy()
		ws.Status = updated.Status
		logger.Info("Updating Workspace from ClusterWorkspace", "reason", "status")
		if _, err = r.updateWorkspaceStatusWithoutProjection(ctx, logicalcluster.From(cw), ws); err != nil {
			return reconcileStatusStopAndRequeue, err
		}
	}

	return reconcileStatusContinue, nil
}
