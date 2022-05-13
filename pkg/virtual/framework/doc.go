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

// Package framework provides a the required interfaces, structs and generic implementation
// that allow creating KCP virtual workspaces with a minimal amount of work.
//
// To create virtual workspaces you have to:
//
// - define the implementation of the VirtualWorkspaces you want to expose (for example with utilities found in the `fixedgvs` or `dynamic` packages)
//
// - define the sub-command that will expose the related CLI arguments, Bootstrap and start those VirtualWorkspaces.
package framework
