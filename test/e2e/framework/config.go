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

package framework

import (
	"flag"
)

type testConfig struct {
	InProcessControllers bool
}

var TestConfig *testConfig

func init() {
	TestConfig = &testConfig{}
	registerFlags(TestConfig)
	// The testing package will call flags.Parse()
}

func registerFlags(c *testConfig) {
	flag.BoolVar(&c.InProcessControllers, "in-process-controllers", false,
		"Whether controllers should be instantiated in the same process as the tests that require them.")
}
