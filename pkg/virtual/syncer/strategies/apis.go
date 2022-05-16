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

package strategies

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type RequestedSyncingState string

const (
	Wait RequestedSyncingState = ""
	Sync RequestedSyncingState = "Sync"
)

type SyncTargetSyncing struct {
	RequestedState    RequestedSyncingState
	Detail            string
	DeletionTimestamp *metav1.Time
	Finalizers        string
}

func (wcs SyncTargetSyncing) RequiredDeletion() *metav1.Time {
	if wcs.DeletionTimestamp != nil && wcs.Finalizers == "" {
		return wcs.DeletionTimestamp
	}
	return nil
}

func (wcs SyncTargetSyncing) Active() bool {
	return wcs.RequestedState == "Sync"
}

type Syncing map[string]SyncTargetSyncing

type SyncStrategy struct {
	ReadFromKCP          func(workloadClusterName string, newKCPResource, existingSyncerViewResource *unstructured.Unstructured, requestedSyncing Syncing) (newSyncerViewResource *unstructured.Unstructured, err error)
	UpdateFromSyncTarget func(workloadClusterName string, newSyncerViewResource *unstructured.Unstructured, existingKCPResource *unstructured.Unstructured, existingSyncerViewResources map[string]unstructured.Unstructured, requestedPlacement Syncing) (newKCPResource *unstructured.Unstructured, err error)
}
