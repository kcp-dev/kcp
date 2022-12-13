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

package helpers

import (
	"bytes"
	"io"
	"sync"
)

func NewHeadWriter(file, out io.Writer) *headWriter {
	return &headWriter{
		file:     file,
		out:      out,
		stopCh:   make(chan struct{}),
		stopOnce: sync.Once{},
	}
}

// headWriter writes to file and out, but stops the later when the channel is closed.
type headWriter struct {
	file, out io.Writer
	stopCh    chan struct{}
	stopOnce  sync.Once

	linePending bool
}

// StopOut stops writing to out writer, but keep writing to file.
func (hw *headWriter) StopOut() {
	hw.stopOnce.Do(func() {
		close(hw.stopCh)
	})
}

func (hw *headWriter) Write(p []byte) (n int, err error) {
	select {
	case <-hw.stopCh:
		if !hw.linePending {
			return hw.file.Write(p)
		}
		if pos := bytes.Index(p, []byte("\n")); pos == -1 {
			hw.out.Write(p) //nolint:errcheck
		} else {
			hw.linePending = false
			hw.out.Write(p[:pos+1]) //nolint:errcheck
		}
	default:
		hw.out.Write(p) //nolint:errcheck
	}
	return hw.file.Write(p)
}
