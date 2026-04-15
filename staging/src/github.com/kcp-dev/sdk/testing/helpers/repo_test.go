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

package helpers

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestRepositoryDir(t *testing.T) {
	origSourceFile := sourceFile
	origFrameFiles := frameFiles
	origReadFile := readFile
	origStatIsDir := statIsDir
	defer func() {
		sourceFile = origSourceFile
		frameFiles = origFrameFiles
		readFile = origReadFile
		statIsDir = origStatIsDir
	}()

	tests := []struct {
		name          string
		envVar        string
		statIsDirF    func(string) (string, bool, error)
		sourceFileF   func() string
		frames        []string
		readFileF     func(string) ([]byte, error)
		want          string
		wantErrSubstr string
	}{
		{
			name:       "env var set to valid directory",
			envVar:     "/mock/repo",
			statIsDirF: func(string) (string, bool, error) { return "/mock/repo", true, nil },
			want:       "/mock/repo",
		},
		{
			name:   "env var set but stat fails",
			envVar: "/mock/repo",
			statIsDirF: func(string) (string, bool, error) {
				return "", false, errors.New("permission denied")
			},
			wantErrSubstr: "could not stat KCP_REPO_DIR",
		},
		{
			name:          "env var set but path is not a directory",
			envVar:        "/mock/repo/file.txt",
			statIsDirF:    func(string) (string, bool, error) { return "", false, nil },
			wantErrSubstr: "is not a directory",
		},
		{
			name:   "caller outside cache, go.mod in same dir",
			envVar: "",
			frames: []string{"/work/repo/pkg/foo.go"},
			readFileF: func(path string) ([]byte, error) {
				if path == "/work/repo/pkg/go.mod" {
					return []byte("module github.com/kcp-dev/kcp\n"), nil
				}
				return nil, os.ErrNotExist
			},
			want: "/work/repo/pkg",
		},
		{
			name:   "caller outside cache, go.mod one level up",
			envVar: "",
			frames: []string{"/work/repo/sub/foo.go"},
			readFileF: func(path string) ([]byte, error) {
				if path == "/work/repo/go.mod" {
					return []byte("module github.com/kcp-dev/kcp\n"), nil
				}
				return nil, os.ErrNotExist
			},
			want: "/work/repo",
		},
		{
			name:   "go.mod with wrong module, correct one higher up",
			envVar: "",
			frames: []string{"/work/repo/sub/foo.go"},
			readFileF: func(path string) ([]byte, error) {
				if path == "/work/repo/sub/go.mod" {
					return []byte("module github.com/other/mod\n"), nil
				}
				if path == "/work/repo/go.mod" {
					return []byte("module github.com/kcp-dev/kcp\n"), nil
				}
				return nil, os.ErrNotExist
			},
			want: "/work/repo",
		},
		{
			name:   "all callers in module cache, sourceFile finds go.mod",
			envVar: "",
			sourceFileF: func() string {
				return "/work/repo/staging/src/github.com/kcp-dev/sdk/testing/helpers/repo.go"
			},
			frames: []string{
				"/home/user/go/pkg/mod/example.com@v1/foo.go",
				"/home/user/go/pkg/mod/other@v2/bar.go",
			},
			readFileF: func(path string) ([]byte, error) {
				if path == "/work/repo/go.mod" {
					return []byte("module github.com/kcp-dev/kcp\n"), nil
				}
				if path == "/work/repo/staging/src/github.com/kcp-dev/sdk/go.mod" {
					return []byte("module github.com/kcp-dev/sdk\n"), nil
				}
				return nil, os.ErrNotExist
			},
			want: "/work/repo",
		},
		{
			name:        "sourceFile in module cache, stack caller finds go.mod",
			envVar:      "",
			sourceFileF: func() string { return "/home/user/go/pkg/mod/github.com/kcp-dev/sdk@v0.1.0/testing/helpers/repo.go" },
			frames: []string{
				"/work/kcp/test/e2e/framework/kcp.go",
			},
			readFileF: func(path string) ([]byte, error) {
				if path == "/work/kcp/go.mod" {
					return []byte("module github.com/kcp-dev/kcp\n"), nil
				}
				if path == "/home/user/go/pkg/mod/github.com/kcp-dev/sdk@v0.1.0/go.mod" {
					return []byte("module github.com/kcp-dev/sdk\n"), nil
				}
				return nil, os.ErrNotExist
			},
			want: "/work/kcp",
		},
		{
			name:   "all callers in module cache, sourceFile empty, no go.mod",
			envVar: "",
			frames: []string{
				"/home/user/go/pkg/mod/example.com@v1/foo.go",
				"/home/user/go/pkg/mod/other@v2/bar.go",
			},
			readFileF: func(path string) ([]byte, error) {
				return nil, os.ErrNotExist
			},
			wantErrSubstr: "could not find the kcp repository root",
		},
		{
			name:   "all callers in build cache, sourceFile finds go.mod",
			envVar: "",
			sourceFileF: func() string {
				return "/work/repo/staging/src/github.com/kcp-dev/sdk/testing/helpers/repo.go"
			},
			frames: []string{
				"/home/user/.cache/go-build/ab/cd/foo.go",
			},
			readFileF: func(path string) ([]byte, error) {
				if path == "/work/repo/go.mod" {
					return []byte("module github.com/kcp-dev/kcp\n"), nil
				}
				if path == "/work/repo/staging/src/github.com/kcp-dev/sdk/go.mod" {
					return []byte("module github.com/kcp-dev/sdk\n"), nil
				}
				return nil, os.ErrNotExist
			},
			want: "/work/repo",
		},
		{
			name:   "caller in build cache, non-cache caller found later",
			envVar: "",
			frames: []string{
				"/home/user/.cache/go-build/ab/cd/foo.go",
				"/work/repo/bar.go",
			},
			readFileF: func(path string) ([]byte, error) {
				if path == "/work/repo/go.mod" {
					return []byte("module github.com/kcp-dev/kcp\n"), nil
				}
				return nil, os.ErrNotExist
			},
			want: "/work/repo",
		},
		{
			name:   "first non-cache frame has no kcp go.mod, later frame does",
			envVar: "",
			frames: []string{
				"/usr/local/go/src/runtime/extern.go",
				"/work/repo/staging/src/github.com/kcp-dev/sdk/testing/helpers/repo.go",
				"/work/repo/test/e2e/framework/kcp.go",
			},
			readFileF: func(path string) ([]byte, error) {
				if path == "/work/repo/go.mod" {
					return []byte("module github.com/kcp-dev/kcp\n"), nil
				}
				if path == "/work/repo/staging/src/github.com/kcp-dev/sdk/go.mod" {
					return []byte("module github.com/kcp-dev/sdk\n"), nil
				}
				return nil, os.ErrNotExist
			},
			want: "/work/repo",
		},
		{
			name:          "single frame without kcp go.mod",
			envVar:        "",
			frames:        []string{"/tmp/foo.go"},
			readFileF:     func(string) ([]byte, error) { return nil, os.ErrNotExist },
			wantErrSubstr: "could not find the kcp repository root",
		},
		{
			name:          "empty frame list",
			envVar:        "",
			frames:        []string{},
			readFileF:     func(string) ([]byte, error) { return nil, os.ErrNotExist },
			wantErrSubstr: "could not find the kcp repository root",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("KCP_REPO_DIR", tt.envVar)

			if tt.statIsDirF != nil {
				statIsDir = tt.statIsDirF
			} else {
				statIsDir = func(string) (string, bool, error) {
					return "", false, errors.New("should not be called")
				}
			}
			if tt.sourceFileF != nil {
				sourceFile = tt.sourceFileF
			} else {
				sourceFile = func() string { return "" }
			}
			frameFiles = func() []string { return tt.frames }
			readFile = tt.readFileF

			got, err := RepositoryDir()

			if tt.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErrSubstr)
				}
				if !strings.Contains(err.Error(), tt.wantErrSubstr) {
					t.Fatalf("expected error containing %q, got: %v", tt.wantErrSubstr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStatIsDirImpl(t *testing.T) {
	origOsStat := osStat
	defer func() { osStat = origOsStat }()

	tests := []struct {
		name      string
		statFunc  func(string) (os.FileInfo, error)
		wantAbs   string
		wantIsDir bool
		wantErr   bool
	}{
		{
			name: "is a directory",
			statFunc: func(string) (os.FileInfo, error) {
				return &mockFileInfo{isDir: true}, nil
			},
			wantIsDir: true,
		},
		{
			name: "is not a directory",
			statFunc: func(string) (os.FileInfo, error) {
				return &mockFileInfo{isDir: false}, nil
			},
			wantIsDir: false,
		},
		{
			name: "stat error",
			statFunc: func(string) (os.FileInfo, error) {
				return nil, os.ErrNotExist
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			osStat = tt.statFunc
			gotAbs, gotIsDir, err := statIsDirImpl("/some/path")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotIsDir != tt.wantIsDir {
				t.Fatalf("IsDir: got %v, want %v", gotIsDir, tt.wantIsDir)
			}
			if gotAbs == "" {
				t.Fatalf("expected non-empty absolute path, got empty string")
			}
			_ = gotAbs
		})
	}
}

type mockFileInfo struct {
	os.FileInfo
	isDir bool
}

func (m *mockFileInfo) IsDir() bool { return m.isDir }
