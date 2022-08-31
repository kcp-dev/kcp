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

package forwardingregistry

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestUpdateToCreateOptions(t *testing.T) {
	updateOptions := reflect.TypeOf(metav1.UpdateOptions{})
	numField := updateOptions.NumField()
	require.Equalf(t, 4, numField, "UpdateOptions is expected to have 4 fields")

	var fields []string
	for i := 0; i < numField; i++ {
		fields = append(fields, updateOptions.Field(i).Name)
	}

	// Assert the UpdateOptions struct fields set has not changed
	expectedFields := []string{
		"TypeMeta",
		"DryRun",
		"FieldManager",
		"FieldValidation",
	}
	require.ElementsMatchf(t, expectedFields, fields, "UpdateOptions struct fields have changed")

	// Assert the CreateOptions fields match that of the UpdateOptions
	uo := &metav1.UpdateOptions{
		DryRun: []string{
			"All",
		},
		FieldManager:    "manager",
		FieldValidation: "Strict",
	}
	co := updateToCreateOptions(uo)

	expectedCreateOptions := metav1.CreateOptions{
		TypeMeta: metav1.TypeMeta{
			Kind:       "CreateOptions",
			APIVersion: "meta.k8s.io/v1",
		},
		DryRun: []string{
			"All",
		},
		FieldManager:    "manager",
		FieldValidation: "Strict",
	}
	require.Equalf(t, expectedCreateOptions, co, "CreateOptions should have the same fields as the UpdateOptions")
}
