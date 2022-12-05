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

package initialization

import (
	"regexp"
	"strings"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"
)

func TestGenerateAPIBindingName(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		exportName           string
		expectedPrefixLength int
	}{
		"short export name": {
			exportName:           "a",
			expectedPrefixLength: 1,
		},
		"max length without truncation": {
			exportName:           strings.Repeat("a", 247),
			expectedPrefixLength: 247,
		},
		"over max length": {
			exportName:           strings.Repeat("a", 248),
			expectedPrefixLength: 247,
		},
	}

	re := regexp.MustCompile(`^(a+)-(.+)$`)

	for testName, tc := range tests {
		t.Run(testName, func(t *testing.T) {
			clusterName := logicalcluster.New("root:some:ws")
			exportPath := "root:some:export:ws"

			generated := generateAPIBindingName(clusterName, exportPath, tc.exportName)
			t.Logf("generated: %s", generated)

			matches := re.FindStringSubmatch(generated)
			require.Len(t, matches, 3)
			require.Len(t, matches[1], tc.expectedPrefixLength)
			require.Len(t, matches[2], 5)
		})
	}
}

func TestGenerateAPIBindingNameWithMultipleSimilarLongNames(t *testing.T) {
	t.Parallel()

	clusterName := logicalcluster.New("root:some:ws")
	exportPath := "root:some:export:ws"

	// 252 chars
	longName1 := "thisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongname"
	// 263 chars
	longName2 := "thisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethatdiffers"
	generated1 := generateAPIBindingName(clusterName, exportPath, longName1)
	t.Logf("generated1: %s", generated1)
	generated2 := generateAPIBindingName(clusterName, exportPath, longName2)
	t.Logf("generated2: %s", generated2)
	require.Len(t, generated1, 253)
	require.Len(t, generated2, 253)
	require.NotEqual(t, generated1, generated2, "expected different generated names")
}
