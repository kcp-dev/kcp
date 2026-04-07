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

package measurement

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// Parameter is a key-value pair used to describe test metadata
// (e.g. workspace count, target QPS) in a section or report header.
type Parameter struct {
	Key   string
	Value string
}

// Section pairs a human-readable title with the Sink that collected
// measurements for that phase of the load test.
type Section struct {
	Title         string
	Parameters    []Parameter
	TotalDuration time.Duration
	Errors        []error
	Sink          Sink

	startTime time.Time
}

// Start records the current time as the start of this section.
func (s *Section) Start() {
	s.startTime = time.Now()
}

// End records the total duration since Start was called.
func (s *Section) End() {
	s.TotalDuration = time.Since(s.startTime)
}

// Report aggregates multiple measurement sections and can pretty-print
// them as a single formatted table.
type Report struct {
	Title    string
	Metadata []Parameter
	Sections []Section
}

func NewReport(title string) *Report {
	return &Report{
		Title: title,
		Metadata: []Parameter{
			{Key: "Date", Value: time.Now().Format(time.RFC1123)},
		},
	}
}

// PrettyPrint writes all sections to w as a formatted table.
// Each section is printed with its title as a header followed by the
// its parameters andkey/value results from the associated Sink.
func (r *Report) PrettyPrint(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	fmt.Fprintf(tw, "\n=== %s ===\n", r.Title)
	for _, m := range r.Metadata {
		fmt.Fprintf(tw, "  %s:\t%s\n", m.Key, m.Value)
	}
	fmt.Fprintln(tw)

	for i, sec := range r.Sections {
		// separate subsequent sections with a blank line for readability
		if i > 0 {
			fmt.Fprintln(tw)
		}

		fmt.Fprintf(tw, "=== %s ===\n", sec.Title)

		// --- Parameters ---
		for _, p := range sec.Parameters {
			fmt.Fprintf(tw, "  %s:\t%s\n", p.Key, p.Value)
		}
		if sec.TotalDuration > 0 {
			fmt.Fprintf(tw, "  Total Duration:\t%s\n", sec.TotalDuration.Round(time.Millisecond))
		}

		// --- Results ---
		results := sec.Sink.Results()
		keys := make([]string, 0, len(results))
		for k := range results {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		fmt.Fprintln(tw)
		fmt.Fprintf(tw, "  Metric\tValue\n")
		fmt.Fprintf(tw, "  ------\t-----\n")
		for _, k := range keys {
			fmt.Fprintf(tw, "  %s\t%f\n", k, results[k])
		}

		// --- Errors ---
		if len(sec.Errors) > 0 {
			fmt.Fprintln(tw)
			fmt.Fprintf(tw, "  Errors: %d\n", len(sec.Errors))
			for _, e := range sec.Errors {
				fmt.Fprintf(tw, "    - %s\n", e.Error())
			}
		}

		fmt.Fprintf(tw, "%s\n", strings.Repeat("-", 40))
	}

	tw.Flush()
}
