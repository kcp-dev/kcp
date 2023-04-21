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

package apibinding

import (
	"sync"

	"k8s.io/apimachinery/pkg/util/sets"
)

// lockedStringSet guards a sets.Set[string] with an RWMutex.
type lockedStringSet struct {
	lock sync.RWMutex
	s    sets.Set[string]
}

func newLockedStringSet(s ...string) *lockedStringSet {
	return &lockedStringSet{
		s: sets.New[string](s...),
	}
}

func (l *lockedStringSet) Add(s string) {
	l.lock.Lock()
	defer l.lock.Unlock()
	l.s.Insert(s)
}

func (l *lockedStringSet) Remove(s string) {
	l.lock.Lock()
	defer l.lock.Unlock()
	l.s.Delete(s)
}

func (l *lockedStringSet) Has(s string) bool {
	l.lock.RLock()
	defer l.lock.RUnlock()
	return l.s.Has(s)
}
