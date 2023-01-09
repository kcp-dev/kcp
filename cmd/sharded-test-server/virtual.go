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

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/abiosoft/lineprefix"
	"github.com/fatih/color"

	"github.com/kcp-dev/kcp/cmd/test-server/helpers"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func startVirtual(ctx context.Context, index int, logDirPath, workDirPath string) (<-chan error, error) {
	prefix := fmt.Sprintf("VW-%d", index)
	yellow := color.New(color.BgYellow, color.FgHiWhite).SprintFunc()
	out := lineprefix.New(
		lineprefix.Prefix(yellow(prefix)),
		lineprefix.Color(color.New(color.FgHiYellow)),
	)

	authenticationKubeconfigPath := filepath.Join(workDirPath, fmt.Sprintf(".kcp-%d", index), "admin.kubeconfig")
	clientCAFilePath := filepath.Join(workDirPath, ".kcp", "client-ca.crt")

	commandLine := framework.DirectOrGoRunCommand("virtual-workspaces")
	commandLine = append(
		commandLine,
		fmt.Sprintf("--kubeconfig=%s", authenticationKubeconfigPath),
		fmt.Sprintf("--authentication-kubeconfig=%s", authenticationKubeconfigPath),
		"--authentication-skip-lookup",
		fmt.Sprintf("--client-ca-file=%s", clientCAFilePath),
		fmt.Sprintf("--tls-private-key-file=%s", filepath.Join(workDirPath, ".kcp/serving-ca.key")),
		fmt.Sprintf("--tls-cert-file=%s", filepath.Join(workDirPath, ".kcp/serving-ca.crt")),
		fmt.Sprintf("--secure-port=%d", 7444+index),
		"--requestheader-username-headers=X-Remote-User",
		"--requestheader-group-headers=X-Remote-Group",
		fmt.Sprintf("--requestheader-client-ca-file=%s", filepath.Join(workDirPath, ".kcp/requestheader-ca.crt")),
		"--v=4",
	)
	fmt.Fprintf(out, "running: %v\n", strings.Join(commandLine, " "))

	cmd := exec.CommandContext(ctx, commandLine[0], commandLine[1:]...) //nolint:gosec

	logFilePath := filepath.Join(logDirPath, fmt.Sprintf("kcp-virtual-workspaces-%d.log", index))
	if err := os.MkdirAll(filepath.Dir(logFilePath), 0755); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(logFilePath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}

	writer := helpers.NewHeadWriter(logFile, out)
	cmd.Stdout = writer
	cmd.Stdin = os.Stdin
	cmd.Stderr = writer

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	terminatedCh := make(chan error, 1)
	go func() {
		terminatedCh <- cmd.Wait()
	}()

	return terminatedCh, nil
}
