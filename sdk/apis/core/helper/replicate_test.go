/*
Copyright 2023 The KCP Authors.

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

package helper

import (
	"reflect"
	"testing"

	"github.com/kcp-dev/kcp/sdk/apis/core"
)

func TestReplicateForValue(t *testing.T) {
	tests := []struct {
		replicateValue  string
		controller      string
		expectedResult  string
		expectedChanged bool
	}{
		{"", "controller1", "controller1", true},
		{"controller1", "controller1", "controller1", false},
		{"controller1", "controller2", "controller1,controller2", true},
		{"controller1,controller2", "controller2", "controller1,controller2", false},
		{"controller1,controller2,controller3", "controller4", "controller1,controller2,controller3,controller4", true},
	}

	for _, test := range tests {
		result, changed := ReplicateForValue(test.replicateValue, test.controller)
		if result != test.expectedResult || changed != test.expectedChanged {
			t.Errorf("ReplicateForValue(%q, %q) = (%q, %t), expected (%q, %t)", test.replicateValue, test.controller, result, changed, test.expectedResult, test.expectedChanged)
		}
	}
}

func TestDontReplicateForValue(t *testing.T) {
	tests := []struct {
		replicateValue  string
		controller      string
		expectedResult  string
		expectedChanged bool
	}{
		{"", "controller1", "", false},
		{"controller1", "controller1", "", true},
		{"controller1,controller2", "controller1", "controller2", true},
		{"controller1,controller2", "controller2", "controller1", true},
		{"controller1,controller2,controller3", "controller4", "controller1,controller2,controller3", false},
	}

	for _, test := range tests {
		result, changed := DontReplicateForValue(test.replicateValue, test.controller)
		if result != test.expectedResult || changed != test.expectedChanged {
			t.Errorf("DontReplicateForValue(%q, %q) = (%q, %t), expected (%q, %t)", test.replicateValue, test.controller, result, changed, test.expectedResult, test.expectedChanged)
		}
	}
}

func TestReplicateFor(t *testing.T) {
	tests := []struct {
		name            string
		labels          map[string]string
		controller      string
		expected        map[string]string
		expectedChanged bool
	}{
		{
			name:       "Add new replicate label to nil map",
			labels:     nil,
			controller: "my-controller",
			expected: map[string]string{
				core.ReplicateAnnotationKey: "my-controller",
			},
			expectedChanged: true,
		},
		{
			name:       "Add new replicate label",
			labels:     map[string]string{},
			controller: "my-controller",
			expected: map[string]string{
				core.ReplicateAnnotationKey: "my-controller",
			},
			expectedChanged: true,
		},
		{
			name: "Add controller to existing replicate label",
			labels: map[string]string{
				core.ReplicateAnnotationKey: "controller-1,controller-2",
			},
			controller: "my-controller",
			expected: map[string]string{
				core.ReplicateAnnotationKey: "controller-1,controller-2,my-controller",
			},
			expectedChanged: true,
		},
		{
			name: "Controller already exists in replicate label",
			labels: map[string]string{
				core.ReplicateAnnotationKey: "my-controller,controller-1,controller-2",
			},
			controller: "my-controller",
			expected: map[string]string{
				core.ReplicateAnnotationKey: "my-controller,controller-1,controller-2",
			},
			expectedChanged: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, changed := ReplicateFor(test.labels, test.controller)
			if !reflect.DeepEqual(result, test.expected) {
				t.Errorf("Expected result: %v, got: %v", test.expected, result)
			}
			if changed != test.expectedChanged {
				t.Errorf("Expected changed: %v, got: %v", test.expectedChanged, changed)
			}
		})
	}
}

func TestDontReplicateFor(t *testing.T) {
	tests := []struct {
		name            string
		labels          map[string]string
		controller      string
		expected        map[string]string
		expectedChanged bool
	}{
		{
			name: "Controller not in replicate label",
			labels: map[string]string{
				core.ReplicateAnnotationKey: "controller-1,controller-2",
			},
			controller: "my-controller",
			expected: map[string]string{
				core.ReplicateAnnotationKey: "controller-1,controller-2",
			},
			expectedChanged: false,
		},
		{
			name: "Remove controller from replicate label",
			labels: map[string]string{
				core.ReplicateAnnotationKey: "my-controller,controller-1,controller-2",
			},
			controller: "my-controller",
			expected: map[string]string{
				core.ReplicateAnnotationKey: "controller-1,controller-2",
			},
			expectedChanged: true,
		},
		{
			name: "Remove only controller in replicate label",
			labels: map[string]string{
				core.ReplicateAnnotationKey: "my-controller",
			},
			controller:      "my-controller",
			expected:        map[string]string{},
			expectedChanged: true,
		},
		{
			name:            "Empty replicate label",
			labels:          map[string]string{},
			controller:      "my-controller",
			expected:        map[string]string{},
			expectedChanged: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, changed := DontReplicateFor(test.labels, test.controller)
			if !reflect.DeepEqual(result, test.expected) {
				t.Errorf("Expected result: %v, got: %v", test.expected, result)
			}
			if changed != test.expectedChanged {
				t.Errorf("Expected changed: %v, got: %v", test.expectedChanged, changed)
			}
		})
	}
}
