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

// Package softimpersonation implements "Soft impersonation",
// which allows carrying the whole description of the user an action is done for,
// without really impersonating this user and thus impacting the authorizations
// on the KCP side.
//
// This is particularly useful in virtual workspaces which are connected to KCP as privileged user,
// but act on behalf of a user, for example when crating a workspace through the `workspaces`
// virtual workspace.
package softimpersonation
