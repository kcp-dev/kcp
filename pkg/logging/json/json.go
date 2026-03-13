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

package json

import (
	"github.com/go-logr/logr"
	"go.uber.org/zap/zapcore"

	"k8s.io/component-base/featuregate"
	logsapi "k8s.io/component-base/logs/api/v1"
	upstreamjson "k8s.io/component-base/logs/json"
)

// Factory produces JSON logger instances with ISO8601 timestamps.
type Factory struct{}

var _ logsapi.LogFormatFactory = Factory{}

func (f Factory) Feature() featuregate.Feature {
	return logsapi.LoggingBetaOptions
}

func (f Factory) Create(c logsapi.LoggingConfiguration, o logsapi.LoggingOptions) (logr.Logger, logsapi.RuntimeControl) {
	// We intentionally avoid all os.File.Sync calls. Output is unbuffered,
	// therefore we don't need to flush, and calling the underlying fsync
	// would just slow down writing.
	//
	// The assumption is that logging only needs to ensure that data gets
	// written to the output stream before the process terminates, but
	// doesn't need to worry about data not being written because of a
	// system crash or powerloss.
	stderr := zapcore.Lock(upstreamjson.AddNopSync(o.ErrorStream))

	// Custom encoder config with ISO8601 timestamps
	encoderConfig := &zapcore.EncoderConfig{
		MessageKey: "msg",
		CallerKey:  "caller",
		NameKey:    "logger",
		TimeKey:    "ts",
		// This right here is the only line we want to change compared to upstream.
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	if c.Options.JSON.SplitStream {
		stdout := zapcore.Lock(upstreamjson.AddNopSync(o.InfoStream))
		size := c.Options.JSON.InfoBufferSize.Value()
		if size > 0 {
			// Prevent integer overflow.
			if size > 2*1024*1024*1024 {
				size = 2 * 1024 * 1024 * 1024
			}
			stdout = &zapcore.BufferedWriteSyncer{
				WS:   stdout,
				Size: int(size),
			}
		}

		// stdout for info messages, stderr for errors.
		return upstreamjson.NewJSONLogger(c.Verbosity, stdout, stderr, encoderConfig)
	}

	// Write info messages and errors to stderr to prevent mixing with normal program output.
	return upstreamjson.NewJSONLogger(c.Verbosity, stderr, nil, encoderConfig)
}
