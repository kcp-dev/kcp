/*
Copyright 2022 The kcp Authors.

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

func TestCacheServerPort(t *testing.T) {
	t.Parallel()
	scenarios := []struct {
		n            int
		expectedPort int
	}{
		{0, 8012},
		{1, 8013},
		{2, 8014},
		{3, 8015},
	}
	for _, scenario := range scenarios {
		actual := cacheServerPort(scenario.n)
		if actual != scenario.expectedPort {
			t.Fatalf("unexpected cache server port %d, expected %d, for n = %d", actual, scenario.expectedPort, scenario.n)
		}
	}
}

func TestCacheEtcdClientPort(t *testing.T) {
	t.Parallel()
	scenarios := []struct {
		n            int
		expectedPort int
	}{
		{0, 8100},
		{1, 8102},
		{2, 8104},
		{3, 8106},
	}
	for _, scenario := range scenarios {
		actual := cacheEtcdClientPort(scenario.n)
		if actual != scenario.expectedPort {
			t.Fatalf("unexpected cache etcd client port %d, expected %d, for n = %d", actual, scenario.expectedPort, scenario.n)
		}
	}
}

func TestCacheEtcdPeerPort(t *testing.T) {
	t.Parallel()
	scenarios := []struct {
		n            int
		expectedPort int
	}{
		{0, 8101},
		{1, 8103},
		{2, 8105},
		{3, 8107},
	}
	for _, scenario := range scenarios {
		actual := cacheEtcdPeerPort(scenario.n)
		if actual != scenario.expectedPort {
			t.Fatalf("unexpected cache etcd peer port %d, expected %d, for n = %d", actual, scenario.expectedPort, scenario.n)
		}
	}
}
