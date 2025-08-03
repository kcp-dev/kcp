/*
Copyright 2025 The KCP Authors.

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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	GroupName = "workload.kcp.io"
	Version   = "v1alpha1"

	SyncTargetResource          = "synctargets"
	PlacementResource           = "placements"
	LocationResource            = "locations"
	ResourceImportResource      = "resourceimports"
	ResourceExportResource      = "resourceexports"
	SynctargetHeartbeatResource = "synctargetheartbeats"
)

// SchemeGroupVersion is group version used to register these objects
var SchemeGroupVersion = schema.GroupVersion{Group: GroupName, Version: Version}

// Resource takes an unqualified resource and returns a Group qualified GroupResource
func Resource(resource string) schema.GroupResource {
	return SchemeGroupVersion.WithResource(resource).GroupResource()
}

var (
	// localSchemeBuilder and AddToScheme will stay in k8s.io/kubernetes.
	localSchemeBuilder = &runtime.SchemeBuilder{}
	// Depreciated: use Install instead
	AddToScheme = localSchemeBuilder.AddToScheme
	Install     = localSchemeBuilder.AddToScheme
)

func init() {
	localSchemeBuilder.Register(addKnownTypes, addDefaultingFuncs)
}

// Adds the list of known types to api.Scheme.
func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemeGroupVersion,
		&SyncTarget{},
		&SyncTargetList{},
		&Placement{},
		&PlacementList{},
		&Location{},
		&LocationList{},
		&ResourceImport{},
		&ResourceImportList{},
		&ResourceExport{},
		&ResourceExportList{},
		&SyncTargetHeartbeat{},
		&SyncTargetHeartbeatList{},
	)
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}

func addDefaultingFuncs(scheme *runtime.Scheme) error {
	return nil
}
