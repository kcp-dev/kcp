/*
Copyright 2022 The kcp Authors.

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

package logicalcluster

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
)

type reconcileStatus int

const (
	reconcileStatusStopAndRequeue reconcileStatus = iota
	reconcileStatusContinue
)

type reconciler interface {
	reconcile(ctx context.Context, logicalCluster *corev1alpha1.LogicalCluster) (reconcileStatus, error)
}

func (c *Controller) reconcile(ctx context.Context, logicalCluster *corev1alpha1.LogicalCluster) (bool, error) {
	getOwner := func(ctx context.Context, owner corev1alpha1.LogicalClusterOwner) (metav1.Object, error) {
		gvr, err := ownerGVR(owner)
		if err != nil {
			return nil, err
		}
		cluster := logicalcluster.NewPath(owner.Cluster)
		return c.dynamicExternalClient.Cluster(cluster).Resource(gvr).Namespace(owner.Namespace).Get(ctx, owner.Name, metav1.GetOptions{})
	}
	patchOwner := func(ctx context.Context, owner corev1alpha1.LogicalClusterOwner, patch []byte) error {
		gvr, err := ownerGVR(owner)
		if err != nil {
			return err
		}
		cluster := logicalcluster.NewPath(owner.Cluster)
		_, err = c.dynamicExternalClient.Cluster(cluster).Resource(gvr).Namespace(owner.Namespace).Patch(ctx, owner.Name, types.MergePatchType, patch, metav1.PatchOptions{})
		return err
	}

	// reconcilers which modify Status should be last
	// reconcilers which modify ObjectMeta, need to return reconcileStatusStopAndRequeue on change
	reconcilers := []reconciler{
		&metaDataReconciler{},
		&shardAnnotationReconciler{
			shardName:  c.shardName,
			getOwner:   getOwner,
			patchOwner: patchOwner,
		},
		&terminatorReconciler{},
		&phaseReconciler{clusterContextManager: c.clusterContextManager},
		&urlReconciler{shardExternalURL: c.shardExternalURL},
	}

	var errs []error

	requeue := false
	for _, r := range reconcilers {
		var err error
		var status reconcileStatus
		status, err = r.reconcile(ctx, logicalCluster)
		if err != nil {
			errs = append(errs, err)
		}
		if status == reconcileStatusStopAndRequeue {
			requeue = true
			break
		}
	}

	return requeue, utilerrors.NewAggregate(errs)
}

func ownerGVR(owner corev1alpha1.LogicalClusterOwner) (schema.GroupVersionResource, error) {
	if owner.APIVersion == "" || owner.Resource == "" {
		return schema.GroupVersionResource{}, fmt.Errorf("LogicalCluster owner missing apiVersion or resource")
	}
	var group, version string
	if i := strings.Index(owner.APIVersion, "/"); i >= 0 {
		group = owner.APIVersion[:i]
		version = owner.APIVersion[i+1:]
	} else {
		version = owner.APIVersion
	}
	if version == "" {
		return schema.GroupVersionResource{}, fmt.Errorf("LogicalCluster owner has invalid apiVersion %q", owner.APIVersion)
	}
	return schema.GroupVersionResource{Group: group, Version: version, Resource: owner.Resource}, nil
}
