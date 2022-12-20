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

package v1alpha1

import (
	"crypto/sha256"
	"math/big"

	"github.com/kcp-dev/logicalcluster/v3"
)

// ToSyncTargetKey hashes the SyncTarget workspace and the SyncTarget name to a string that is used to identify
// in a unique way the synctarget in annotations/labels/finalizers.
func ToSyncTargetKey(clusterName logicalcluster.Name, syncTargetName string) string {
	hash := sha256.Sum224([]byte(clusterName.Path().Join(syncTargetName).String()))
	base62hash := toBase62(hash)
	return base62hash
}

func toBase62(hash [28]byte) string {
	var i big.Int
	i.SetBytes(hash[:])
	return i.Text(62)
}
