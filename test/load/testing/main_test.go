/*
Copyright 2026 The kcp Authors.

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

package testing

import (
	"flag"
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	configFile := flag.String("config", "", "path to a JSON config file with load test parameters")
	flag.Parse()

	if err := parseConfig(*configFile); err != nil {
		fmt.Fprintf(os.Stderr, "Error loading parameters: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}
