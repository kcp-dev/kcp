//go:build e2e
// +build e2e

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

package framework

import (
	"fmt"
	"os"
	"path"
	"strings"
)

type User struct {
	Name   string
	Token  string
	UID    string
	Groups []string
}

var LoopbackUser User = User{
	Name: "loopback",
}

func (u User) String() string {
	return fmt.Sprintf("%s,%s,%s,\"%s\"", u.Token, u.Name, u.UID, strings.Join(u.Groups, ","))
}

type Users []User

func (us Users) ArgsForKCP(t TestingTInterface) ([]string, error) {
	kcpTokensPath := path.Join(t.TempDir(), "kcp-tokens")
	kcpTokens, err := os.Create(kcpTokensPath)
	if err != nil {
		return nil, err
	}
	defer kcpTokens.Close()
	for _, user := range us {
		if _, err := kcpTokens.WriteString(user.String() + "\n"); err != nil {
			return nil, err
		}
	}
	return []string{"--token-auth-file", kcpTokensPath}, nil
}
