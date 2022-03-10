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

package compositecmd

/*
import (
	"context"
	"strings"

	"github.com/spf13/pflag"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/kube-openapi/pkg/common"

	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	virtualgenericcmd "github.com/kcp-dev/kcp/pkg/virtual/framework/cmd"
	builders "github.com/kcp-dev/kcp/pkg/virtual/framework/fixedgvs"
	virtualrootapiserver "github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
)

var _ virtualgenericcmd.SubCommandOptions = (*CompositeSubCommandOptions)(nil)

type CompositeSubCommandOptions struct {
	StoragesPerPrefix              map[string]map[schema.GroupVersion]map[string]rest.Storage
	GetOpenAPIDefinitionsPerPrefix map[string]map[schema.GroupVersion]common.GetOpenAPIDefinitions
}

func (o *CompositeSubCommandOptions) Description() virtualgenericcmd.SubCommandDescription {
	return virtualgenericcmd.SubCommandDescription{
		Name:  "composite",
		Use:   "composite",
		Short: "Launch a composite virtual workspace apiserver",
		Long:  "Start a composite virtual workspace apiserver, that supports several prefixed virtual workspaces, with several APIGroups",
	}
}

func (o *CompositeSubCommandOptions) AddFlags(flags *pflag.FlagSet) {
	if o == nil {
		return
	}
}

func (o *CompositeSubCommandOptions) Validate() []error {
	if o == nil {
		return nil
	}
	errs := []error{}
	return errs
}

func (o *CompositeSubCommandOptions) NewVirtualWorkspaces() ([]virtualrootapiserver.InformerStart, []framework.VirtualWorkspace, error) {
	var virtualWorkspaces []framework.VirtualWorkspace
	for prefix, vw := range o.StoragesPerPrefix {
		prefixWithSlash := "/" + prefix
		virtualWorkspace := &builders.FixedGroupVersionsVirtualWorkspace{
			Name: prefix,
			RootPathResolver: func(urlPath string, context context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
				if strings.HasPrefix(urlPath, prefixWithSlash) {
					return true, prefixWithSlash, context
				}
				return false, "", context
			},
			Ready: func() error { return nil },
		}

		getOpenAPIDefintionsForGV := o.GetOpenAPIDefinitionsPerPrefix[prefix]

		for gv, storages := range vw {
			storageBuilders := make(map[string]builders.RestStorageBuilder)
			var addStoragesToScheme []func(*runtime.Scheme) error
			for name, storage := range storages {
				storage := storage
				addStorageToscheme := func(scheme *runtime.Scheme) error {
					obj := storage.New()
					gvk := obj.GetObjectKind().GroupVersionKind()
					scheme.AddKnownTypeWithName(
						gvk,
						obj,
					)
					objList := storage.(rest.Lister).NewList()
					gvkList := objList.GetObjectKind().GroupVersionKind()
					scheme.AddKnownTypeWithName(
						gvkList,
						objList,
					)
					return nil
				}
				addStoragesToScheme = append(addStoragesToScheme, addStorageToscheme)
				storageBuilders[name] = func(apiGroupAPIServerConfig genericapiserver.CompletedConfig) (rest.Storage, error) {
					return storage, nil
				}
			}

			var getOpenAPIDefintions common.GetOpenAPIDefinitions
			if getOpenAPIDefintionsForGV != nil {
				getOpenAPIDefintions = getOpenAPIDefintionsForGV[gv]
			}
			groupVersionAPISet := builders.GroupVersionAPISet{
				GroupVersion: gv,
				AddToScheme: func(scheme *runtime.Scheme) error {
					for _, addToScheme := range addStoragesToScheme {
						if err := addToScheme(scheme); err != nil {
							return err
						}
					}
					return nil
				},
				BootstrapRestResources: func(genericapiserver.CompletedConfig) (map[string]builders.RestStorageBuilder, error) {
					return storageBuilders, nil
				},
				OpenAPIDefinitions: getOpenAPIDefintions,
			}
			virtualWorkspace.GroupVersionAPISets = append(virtualWorkspace.GroupVersionAPISets, groupVersionAPISet)
		}
		virtualWorkspaces = append(virtualWorkspaces, virtualWorkspace)
	}
	return []virtualrootapiserver.InformerStart{},
		virtualWorkspaces,
		nil
}
*/
