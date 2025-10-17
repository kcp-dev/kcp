/*
Copyright 2025 The Kubernetes Authors.
Modifications Copyright 2025 The KCP Authors.

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

package generators

import "k8s.io/gengo/v2/types"

var (
	cacheGenericLister                 = types.Name{Package: "k8s.io/client-go/tools/cache", Name: "GenericLister"}
	cacheIndexers                      = types.Name{Package: "k8s.io/client-go/tools/cache", Name: "Indexers"}
	cacheListWatch                     = types.Name{Package: "k8s.io/client-go/tools/cache", Name: "ListWatch"}
	cacheMetaNamespaceIndexFunc        = types.Name{Package: "k8s.io/client-go/tools/cache", Name: "MetaNamespaceIndexFunc"}
	cacheNamespaceIndex                = types.Name{Package: "k8s.io/client-go/tools/cache", Name: "NamespaceIndex"}
	cacheNewSharedIndexInformer        = types.Name{Package: "k8s.io/client-go/tools/cache", Name: "NewSharedIndexInformer"}
	cacheSharedIndexInformer           = types.Name{Package: "k8s.io/client-go/tools/cache", Name: "SharedIndexInformer"}
	cacheWaitForCacheSync              = types.Name{Package: "k8s.io/client-go/tools/cache", Name: "WaitForCacheSync"}
	scopeableCacheSharedIndexInformer  = types.Name{Package: "github.com/kcp-dev/apimachinery/v2/pkg/cache", Name: "ScopeableSharedIndexInformer"}
	kcpcacheGenericClusterLister       = types.Name{Package: "github.com/kcp-dev/apimachinery/v2/pkg/cache", Name: "GenericClusterLister"}
	kcpcacheNewGenericClusterLister    = types.Name{Package: "github.com/kcp-dev/apimachinery/v2/pkg/cache", Name: "NewGenericClusterLister"}
	kcpinformersNewSharedIndexInformer = types.Name{Package: "github.com/kcp-dev/apimachinery/v2/third_party/informers", Name: "NewSharedIndexInformer"}
	cacheTransformFunc                 = types.Name{Package: "k8s.io/client-go/tools/cache", Name: "TransformFunc"}
	contextContext                     = types.Name{Package: "context", Name: "Context"}
	contextBackgroundFunc              = types.Name{Package: "context", Name: "Background"}
	fmtErrorfFunc                      = types.Name{Package: "fmt", Name: "Errorf"}
	reflectType                        = types.Name{Package: "reflect", Name: "Type"}
	reflectTypeOf                      = types.Name{Package: "reflect", Name: "TypeOf"}
	runtimeObject                      = types.Name{Package: "k8s.io/apimachinery/pkg/runtime", Name: "Object"}
	schemaGroupResource                = types.Name{Package: "k8s.io/apimachinery/pkg/runtime/schema", Name: "GroupResource"}
	schemaGroupVersionResource         = types.Name{Package: "k8s.io/apimachinery/pkg/runtime/schema", Name: "GroupVersionResource"}
	syncMutex                          = types.Name{Package: "sync", Name: "Mutex"}
	syncWaitGroup                      = types.Name{Package: "sync", Name: "WaitGroup"}
	timeDuration                       = types.Name{Package: "time", Name: "Duration"}
	metav1ListOptions                  = types.Name{Package: "k8s.io/apimachinery/pkg/apis/meta/v1", Name: "ListOptions"}
	metav1NamespaceAll                 = types.Name{Package: "k8s.io/apimachinery/pkg/apis/meta/v1", Name: "NamespaceAll"}
	metav1Object                       = types.Name{Package: "k8s.io/apimachinery/pkg/apis/meta/v1", Name: "Object"}
	watchInterface                     = types.Name{Package: "k8s.io/apimachinery/pkg/watch", Name: "Interface"}
	logicalclusterName                 = types.Name{Package: "github.com/kcp-dev/logicalcluster/v3", Name: "Name"}
)
