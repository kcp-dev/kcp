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

package revdial

// define the constants used to build the URL
// The listener connects to an user with path [host:port/base]/revdial?id=[id]
// The dialer listens on the urls:
// [host:port/base]/revdial for the reverse connections
// [host:port/base]/proxy/[id]/[path] for the reverse proxied to [path]
const (
	pathRevDial           = "revdial"
	pathRevProxy          = "proxy"
	urlParamKey           = "id"
	DefaultRootPathPrefix = "/tunneler"
)
