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

package apiresourceschema

import (
	"reflect"
	"testing"
)

func TestValidationOptionDrift(t *testing.T) {
	expectedNonBool := map[string]reflect.Kind{
		"DisallowDefaultsReason": reflect.String,
		"CELEnvironmentSet":      reflect.Ptr,
		"PreexistingExpressions": reflect.Struct,
	}
	expectedFalse := map[string]bool{
		"RequireImmutableNames": true,
	}

	v := reflect.ValueOf(defaultValidationOpts)
	for _, f := range reflect.VisibleFields(reflect.TypeOf(defaultValidationOpts)) {
		if f.Type.Kind() != reflect.Bool {
			if expected, found := expectedNonBool[f.Name]; found {
				if f.Type.Kind() != expected {
					t.Errorf("expected %s to be %s, got %s", f.Name, expected, f.Type.Kind())
				}
			} else {
				t.Errorf("unexpected field %s of type %s", f.Name, f.Type.Kind())
			}
			continue
		} else if fieldValue := v.FieldByIndex(f.Index).Bool(); !fieldValue && !expectedFalse[f.Name] {
			t.Errorf("unexpected value `true` for field %s, got `false`, probably newly added", f.Name)
		}
	}
}
