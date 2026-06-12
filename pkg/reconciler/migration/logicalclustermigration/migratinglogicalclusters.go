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

package logicalclustermigration

import (
	"fmt"
	"sync"

	"github.com/kcp-dev/logicalcluster/v3"
)

// MigratingLogicalClusters tracks which logical clusters are in migration.
type MigratingLogicalClusters struct {
	clusters sync.Map // logicalcluster.Name → string (migration object path)
}

func NewMigratingLogicalClusters() *MigratingLogicalClusters {
	return &MigratingLogicalClusters{}
}

// Set marks a logical cluster as migrating.
// The migrationPath is the path to the LogicalClusterMigration that triggered
// the migration in the form <workspace path>:<migration name>.
func (m *MigratingLogicalClusters) Set(name logicalcluster.Name, migrationPath string) error {
	cur, loaded := m.clusters.LoadOrStore(name, migrationPath)
	if loaded && cur != migrationPath {
		return fmt.Errorf("tried to register logical cluster %s as migrating that was already in migration from a different path: existing=%s new=%s", name, cur, migrationPath)
	}
	return nil
}

// Remove marks a logical cluster as no longer migrating.
func (m *MigratingLogicalClusters) Remove(name logicalcluster.Name) {
	m.clusters.Delete(name)
}

// Get returns the migration object path for a migrating logical cluster.
func (m *MigratingLogicalClusters) Get(name logicalcluster.Name) (string, bool) {
	v, ok := m.clusters.Load(name)
	if !ok {
		return "", false
	}
	return v.(string), true
}

// IsMigrating returns whether the logical cluster is currently being migrated.
func (m *MigratingLogicalClusters) IsMigrating(name logicalcluster.Name) bool {
	_, ok := m.clusters.Load(name)
	return ok
}
