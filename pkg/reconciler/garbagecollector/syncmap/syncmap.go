/*
Copyright 2025 The KCP Authors.

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

package syncmap

import "sync"

// SyncMap is a type-safe wrapper around sync.Map.
//
// The sync.Map is backed by a generic map but this is hidden away in an
// internal package. Once sync/v2 drops that will likely have a generic
// version of sync.Map that his can be replaced with:
// https://github.com/golang/go/issues/71076
type SyncMap[K comparable, V any] struct {
	m sync.Map
}

// NewSyncMap creates a new SyncMap.
func NewSyncMap[K comparable, V any]() *SyncMap[K, V] {
	return &SyncMap[K, V]{
		m: sync.Map{},
	}
}

// Store stores the given key-value pair in the map.
func (m *SyncMap[K, V]) Store(key K, value V) {
	m.m.Store(key, value)
}

// StoreIfAbsent stores the given key-value pair in the map only if
// the key does not already exist.
func (m *SyncMap[K, V]) StoreIfAbsent(key K, value V) {
	m.m.LoadOrStore(key, value)
}

// Load retrieves the value for the given key from the map.
func (m *SyncMap[K, V]) Load(key K) (V, bool) {
	value, ok := m.m.Load(key)
	if !ok {
		return *new(V), false
	}
	return value.(V), true
}

// LoadAndDelete retrieves and removes the value for the given key from
// the map.
func (m *SyncMap[K, V]) LoadAndDelete(key K) (V, bool) {
	value, ok := m.m.LoadAndDelete(key)
	if !ok {
		return *new(V), false
	}
	return value.(V), true
}

// Delete removes the given key from the map.
func (m *SyncMap[K, V]) Delete(key K) {
	m.m.Delete(key)
}

// Modify modifies the value for the given key using the provided
// modifyFunc.
// The modifyFunc receives the current value or a valid default value
// and a boolean indicating whether the key exists in the map. It should
// return the new value to be stored.
// Modify returns true if the value was successfully updated, false otherwise.
func (m *SyncMap[K, V]) Modify(key K, modifyFunc func(value V, exists bool) V) bool {
	value, exists := m.Load(key)
	if !exists {
		value = *new(V)
	}
	newValue := modifyFunc(value, exists)
	return m.m.CompareAndSwap(key, value, newValue)
}

// Range iterates over all key-value pairs in the map, calling the
// provided function for each pair. If the function returns false,
// the iteration stops.
func (m *SyncMap[K, V]) Range(f func(key K, value V) bool) {
	m.m.Range(func(k, v any) bool {
		return f(k.(K), v.(V))
	})
}
