/*
Copyright 2023 The KCP Authors.

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

package plugin

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type runCmdOption func(cmd *exec.Cmd) *exec.Cmd

func withEnvironExcept(ignore ...string) runCmdOption {
	return func(cmd *exec.Cmd) *exec.Cmd {
		for _, v := range os.Environ() {
			skip := false
			for _, i := range ignore {
				if strings.HasPrefix(v, fmt.Sprintf("%s=", i)) {
					skip = true
				}
			}
			if !skip {
				cmd.Env = append(cmd.Env, v)
			}
		}
		return cmd
	}
}
func withEnvVariable(name, value string) runCmdOption {
	return func(cmd *exec.Cmd) *exec.Cmd {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", name, value))
		return cmd
	}
}

func runCmd(commandLine []string, options ...runCmdOption) (*bytes.Buffer, error) {
	cmd := exec.Command(commandLine[0], commandLine[1:]...) //nolint:gosec
	// cmd.Env = os.Environ()
	for _, opt := range options {
		cmd = opt(cmd)
	}

	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		fmt.Printf("stdout:\n%s\nstderr:\n%s\n", outb.String(), errb.String())
		return nil, fmt.Errorf("failed to run %s", strings.Join(commandLine, " "))
	}
	return &outb, nil
}
