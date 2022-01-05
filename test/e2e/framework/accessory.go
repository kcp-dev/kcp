package framework

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// NewAccessory creates a new accessory process.
func NewAccessory(t TestingTInterface, artifactDir string, cmd string, args ...string) *Accessory {
	return &Accessory{
		t:           t,
		artifactDir: artifactDir,
		cmd:         cmd,
		args:        args,
	}
}

// Accessory knows how to run an executable with arguments for the duration of the context.
type Accessory struct {
	t           TestingTInterface
	artifactDir string
	cmd         string
	args        []string
}

func (a *Accessory) Run(parentCtx context.Context) error {
	ctx, cancel := context.WithCancel(parentCtx)

	if deadline, ok := a.t.Deadline(); ok {
		deadlinedCtx, deadlinedCancel := context.WithDeadline(ctx, deadline.Add(-10*time.Second))
		ctx = deadlinedCtx
		a.t.Cleanup(deadlinedCancel) // this does not really matter but govet is upset
	}
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	a.t.Cleanup(func() {
		a.t.Logf("cleanup: ending `%s`", a.cmd)
		cancel()
		<-cleanupCtx.Done()
	})

	cmd := exec.CommandContext(ctx, a.cmd, a.args...)

	a.t.Logf("running: %v", strings.Join(cmd.Args, " "))
	logFile, err := os.Create(filepath.Join(a.artifactDir, fmt.Sprintf("%s.log", a.cmd)))
	if err != nil {
		cleanupCancel()
		return fmt.Errorf("could not create log file: %w", err)
	}
	log := bytes.Buffer{}
	writers := []io.Writer{&log, logFile}
	mw := io.MultiWriter(writers...)
	cmd.Stdout = mw
	cmd.Stderr = mw
	if err := cmd.Start(); err != nil {
		cleanupCancel()
		return err
	}
	go func() {
		defer func() { cleanupCancel() }()
		err := cmd.Wait()
		if err != nil && ctx.Err() == nil {
			a.t.Errorf("`%s` failed: %w output: %s", a.cmd, err, log)
		}
	}()
	return nil
}
