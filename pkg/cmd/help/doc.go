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
