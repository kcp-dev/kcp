/*
Copyright 2025 The kcp Authors.

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

// Package terminatingworkspaces and its sub-packages provide the Terminating Workspace Virtual Workspace.
//
// It allows for cross-cluster LIST + WATCH of LogicalClusters which:
//   - are marked for deletion via a DeletionTimestamp
//   - request termination by a specific controller
//
// That is, a request for
// GET /services/terminatingworkspaces/<terminator>/clusters/*/apis/core.kcp.io/v1alpha1/logicalclusters
// will return a list of LogicalCluster objects which are Terminating and for which status.terminators contains the
// <terminator-name>.
// WATCH semantics are similar to (and implemented by) timestamp and terminators selectors - a LogicalCluster that stops
// matching the requirements to be served (not being marked for deletion, not requesting termination by
// the controller) will be removed from the stream with a synthetic Deleted event.
/*
         __
 _(\    |@@|
(__/\__ \--/ __
   \___|----|  |   __
       \ }{ /\ )_ / _\
       /\__/\ \__O (__
      (--/\--)    \__/
      _)(  )(_
     `---''---`
*/
package terminatingworkspaces

const VirtualWorkspaceName string = "terminatingworkspaces"
