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

package wait

import (
	"time"

	kwait "k8s.io/apimachinery/pkg/util/wait"
)

// ForeverTestTimeout is the timeout used by the KCP test suite. Kubernetes uses 30s,
// but we have a lot of tests that are slow, so we use 60s.
var ForeverTestTimeout = time.Second * 60

var PollUntilContextTimeout = kwait.PollUntilContextTimeout