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

package help

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/MakeNowJust/heredoc"
	"github.com/muesli/reflow/wordwrap"
	"github.com/spf13/cobra"
	terminal "github.com/wayneashleyberry/terminal-dimensions"
)

var reEmptyLine = regexp.MustCompile(`(?m)([\w[:punct:]])[ ]*\n([\w[:punct:]])`)

func Doc(s string) string {
	s = heredoc.Doc(s)
	s = reEmptyLine.ReplaceAllString(s, "$1 $2")
	return s
}

func FitTerminal() {
	cobra.AddTemplateFunc("trimTrailingWhitespaces", func(s string) string {
		w, err := terminal.Width()
		if err != nil {
			w = 80
		}
		return strings.TrimRightFunc(wordwrap.String(s, int(w)), unicode.IsSpace)
	})
}
