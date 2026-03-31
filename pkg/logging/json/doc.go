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

// Package json exists solely to change the date formatting in the JSON log
// output, which Kubernetes hardcodes to be float-based UNIX timestamps,
// but which we want as much more useful ISO 8601 date strings. Sadly there
// is no way to just configure this without replicating the entire JSON logger.
package json
