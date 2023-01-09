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

package main

import "testing"

func TestEmbeddedEtcdClientPort(t *testing.T) {
	scenarios := []struct {
		n            int
		expectedPort int
	}{
		{
			1,
			2381,
		},
		{
			2,
			2383,
		},
		{
			3,
			2385,
		},
		{
			4,
			2387,
		},
		{
			5,
			2389,
		},
		{
			6,
			2391,
		},
	}

	for _, scenario := range scenarios {
		actualPort := embeddedEtcdClientPort(scenario.n)
		if actualPort != scenario.expectedPort {
			t.Fatalf("unexpected etcd client port %d, expected %d, for n = %d", actualPort, scenario.expectedPort, scenario.n)
		}
	}
}

func TestEmbeddedEtcdPeerPort(t *testing.T) {
	scenarios := []struct {
		n            int
		expectedPort int
	}{
		{
			1,
			2382,
		},
		{
			2,
			2384,
		},
		{
			3,
			2386,
		},
		{
			4,
			2388,
		},
		{
			5,
			2390,
		},
		{
			6,
			2392,
		},
	}

	for _, scenario := range scenarios {
		actualPort := embeddedEtcdPeerPort(scenario.n)
		if actualPort != scenario.expectedPort {
			t.Fatalf("unexpected etcd peer port %d, expected %d, for n = %d", actualPort, scenario.expectedPort, scenario.n)
		}
	}
}
