/*
Copyright 2022 The Kube Bind Authors.

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

package options

import (
	"encoding/base64"
	"fmt"

	"github.com/spf13/pflag"
)

type Cookie struct {
	SigningKey    string
	EncryptionKey string
}

func NewCookie() *Cookie {
	return &Cookie{}
}

func (options *Cookie) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&options.SigningKey, "cookie-signing-key", options.SigningKey, "The key which is used to sign cookies, base64 encoded. Valid lengths are 32 or 64 bytes.")
	fs.StringVar(&options.EncryptionKey, "cookie-encryption-key", options.EncryptionKey, "The key which is used to encrypt cookies, base64 encoded, optional. Valid lengths are 16, 24, or 32 bytes selecting AES-128, AES-192, or AES-256.")
}

func (options *Cookie) Complete() error {
	return nil
}

func (options *Cookie) Validate() error {
	if options.SigningKey == "" {
		return fmt.Errorf("cookie signing key must not be empty")
	}

	if err := checkKey(options.SigningKey, 32, 64); err != nil {
		return fmt.Errorf("invalid signing key: %w", err)
	}

	if options.EncryptionKey != "" {
		if err := checkKey(options.SigningKey, 16, 24, 32); err != nil {
			return fmt.Errorf("invalid encryption key: %w", err)
		}
	}

	return nil
}

func checkKey(key string, validLengths ...int) error {
	b, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return err
	}
	for _, validLength := range validLengths {
		if len(b) == validLength {
			return nil
		}
	}

	return fmt.Errorf("invalid key length: %d", len(b))
}
