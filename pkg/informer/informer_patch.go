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

package informer

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/informers"
)

func (f *dynamicSharedInformerFactory) Remove(gvr schema.GroupVersionResource, informer informers.GenericInformer) {
	f.lock.Lock()
	defer f.lock.Unlock()

	if f.informers[gvr] == informer {
		delete(f.informers, gvr)
	}
}
