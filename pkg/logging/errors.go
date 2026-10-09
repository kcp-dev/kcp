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

package logging

import (
	"context"
	"errors"
	"net/http"
	"syscall"
)

// IsClientDisconnect reports whether err means the client went away rather than
// the server or a backend failing. ctx is the inbound request's context.
//
// A proxy sees this routinely and it is not a fault: a client that cancels a
// watch, closes a kubectl session or simply goes offline cancels the inbound
// request, which cancels the outbound one. Logging that at error level turns
// ordinary client behaviour into a stream of errors that hides real problems, so
// callers log these at a high verbosity instead, and skip writing a response
// that nobody is left to read.
func IsClientDisconnect(ctx context.Context, err error) bool {
	// The inbound request being cancelled is the most direct signal: whatever the
	// outbound error says, there is no client left to serve.
	if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
		return true
	}
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, http.ErrAbortHandler) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET)
}
