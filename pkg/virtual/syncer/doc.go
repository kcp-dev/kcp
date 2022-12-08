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

// Package syncer and its sub-packages provide the Syncer Virtual Workspace.
//
// It exposes an APIserver URL for each SyncTarget hosting a syncer agent,
// with REST endpoints for APIs that have been imported from this SyncTarget and published.
//
// It combines and integrates:
//
// - a controller (APIReconciler) that watches for available APIResourceImports and updates the list of installed APIs
// for the corresponding SyncTarget (in the ./controllers package)
//
// - a DynamicVirtualWorkspace instantiation that exposes and serve installed APIs on the right sync-target-dedicated path
// through CRD-like handlers (in the ../framework/dynamic package)
//
// - a REST storage implementation, named ForwardingREST, that can dynamically serve resources by delegating to
// a KCP workspace-aware client-go dynamic client (in the ../framework/forwardingregistry package)
//
// The builder package is the place where all these components are combined together, especially in the
// BuildVirtualWorkspace() function.
package syncer
