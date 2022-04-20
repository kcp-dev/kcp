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

package apiexport

import (
	"fmt"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

func indexAPIResourceSchemaByWorkspace(obj interface{}) ([]string, error) {
	schema, ok := obj.(*apisv1alpha1.APIResourceSchema)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be an APIResourceSchema, but is %T", obj)
	}

	return []string{logicalcluster.From(schema).String()}, nil
}

func indexNegotiatedAPIResourceByWorkspace(obj interface{}) ([]string, error) {
	resource, ok := obj.(*apiresourcev1alpha1.NegotiatedAPIResource)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be an NegotiatedAPIResource, but is %T", obj)
	}

	return []string{logicalcluster.From(resource).String()}, nil
}
