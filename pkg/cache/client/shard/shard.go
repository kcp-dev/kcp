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

package shard

import (
	"path"
)

const (
	// AnnotationKey is the name of the annotation key used to denote an object's shard name.
	AnnotationKey = "kcp.io/shard"
)

// Name hold the name of a shard. It is used by the cache-server
// to assign resources to a particular shard.
type Name string

var (
	// Wildcard is the name indicating cross-shards requests.
	Wildcard = Name("*")
)

// New returns a new instance of Name from a string.
func New(name string) Name {
	return Name(name)
}

// Path returns a path segment for the shard to access its API.
func (n Name) Path() string {
	return path.Join("/shards", n.String())
}

// String returns the string representation of the shard name.
func (n Name) String() string {
	return string(n)
}

// Empty returns true if the name of the shard is empty.
func (n Name) Empty() bool {
	return n == ""
}
