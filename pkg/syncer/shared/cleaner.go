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

package shared

// Cleaner is an interface for cleaning up resources.
type Cleaner interface {
	// PlanCleaning adds the key to the list of keys to be cleaned up.
	// If the resource was already planned for cleaning the previous timestamp is kept.
	PlanCleaning(key string)
	// CancelCleaning removes the key from the list of keys to be cleaned up.
	// If it wasn't planned for deletion, it does nothing.
	CancelCleaning(key string)
}
