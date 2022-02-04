package main

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"io/ioutil"
	"os"
	"os/exec"
	"syscall"
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
