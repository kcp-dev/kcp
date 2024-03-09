/*
Copyright 2024 The KCP Authors.

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

package proxy

import (
	"reflect"
	"testing"
)

func TestSortMappings(t *testing.T) {
	mappings := []httpHandlerMapping{
		{weight: 3},
		{weight: 1},
		{weight: 2},
		{weight: 10},
	}

	expected := []httpHandlerMapping{
		{weight: 10},
		{weight: 3},
		{weight: 2},
		{weight: 1},
	}

	sortedMappings := sortMappings(mappings)

	if !reflect.DeepEqual(sortedMappings, expected) {
		t.Errorf("Expected %v, but got %v", expected, sortedMappings)
	}
}
