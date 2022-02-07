/*
Copyright 2021 The KCP Authors.

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
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"syscall"

	"gopkg.in/yaml.v3"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Printf("usage: kcp-config [config-file] [cmd] [args...]\n")
		os.Exit(1)
	}

	fname := os.Args[1]

	data, err := ioutil.ReadFile(fname)
	if err != nil {
		fmt.Printf("Error reading config file %s: %v\n", fname, err)
		os.Exit(1)
	}

	contents := map[string]interface{}{}
	err = yaml.Unmarshal([]byte(data), &contents)
	if err != nil {
		fmt.Printf("error: %v\n", err)
		os.Exit(1)
	}

	args := os.Args[2:]
	for k, v := range contents {
		args = append(args, fmt.Sprintf("--%s=%v", k, v))
	}

	cmd, err := exec.LookPath(os.Args[2])

	if err != nil {
		fmt.Printf("kcp executable not found in $PATH\n")
		os.Exit(1)
	}

	if err = syscall.Exec(cmd, args, os.Environ()); err != nil {
		fmt.Printf("kcp exec() failed: %v\n", err)
		os.Exit(1)
	}
}
