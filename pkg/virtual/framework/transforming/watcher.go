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

package transforming

import (
	"k8s.io/apimachinery/pkg/watch"
)

type TransformingWatcher struct {
	source           watch.Interface
	transformedCh    chan watch.Event
	eventTransformer EventTransformer
}

type EventTransformer func(event watch.Event) (transformed *watch.Event)

func NewTransformingWatcher(watcher watch.Interface, eventTransformer EventTransformer) *TransformingWatcher {
	tw := &TransformingWatcher{
		source:           watcher,
		transformedCh:    make(chan watch.Event),
		eventTransformer: eventTransformer,
	}
	tw.start()
	return tw
}

func (w *TransformingWatcher) start() {
	go func() {
		for {
			if evt, more := <-w.source.ResultChan(); more {
				transformedEvent := w.eventTransformer(evt)
				if transformedEvent != nil {
					w.transformedCh <- *transformedEvent
				}
			} else {
				close(w.transformedCh)
				return
			}
		}
	}()
}

// Stop implements Interface
func (w *TransformingWatcher) Stop() {
	w.source.Stop()
}

// ResultChan implements Interface
func (w *TransformingWatcher) ResultChan() <-chan watch.Event {
	return w.transformedCh
}
