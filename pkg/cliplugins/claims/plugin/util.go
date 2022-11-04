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

package plugin

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"strings"
)

// ClaimAction captures the user's preference of a specific action
// on permission claim. They can either accept, reject or ignore/ skip
// ay action on an open permission claim.
type ClaimAction int

const (
	// User has decided to accept claim.
	AcceptClaim ClaimAction = iota
	// User has decided to reject claim.
	RejectClaim
	// User has decided to skip claim.
	SkipClaim
)

// Print the message for the user in the prompt. The resource name in a claim is a required field,
// but the group can be empty.
func printMessage(bindingName, claimGroup, claimResource string) string {
	if claimGroup != "" {
		return fmt.Sprintf("Accept permission claim for Group: %s, Resource: %s (APIBinding: %s) > ", claimGroup, claimResource, bindingName)
	}
	return fmt.Sprintf("Accept permission claim for Resource: %s (APIBinding: %s) > ", claimResource, bindingName)
}

func getRequiredInput(rd io.Reader, wr io.Writer, bindingName, claimGroup, claimResource string) (ClaimAction, error) {
	reader := bufio.NewReader(rd)

	for {
		_, err := fmt.Fprint(wr, printMessage(bindingName, claimGroup, claimResource))
		if err != nil {
			return -1, err
		}

		value := readInput(reader)
		if value != "" {
			return inferText(value, wr)
		}
		_, err = fmt.Fprintf(wr, "Input is required. Enter `skip/s` instead. ")
		if err != nil {
			return -1, err
		}
	}
}

func readInput(reader *bufio.Reader) string {
	for {
		text := readLine(reader)
		return text
	}
}

// readstdin reads a line from stdin and returns the value.
func readLine(reader *bufio.Reader) string {
	text, err := reader.ReadString('\n')
	if err != nil {
		log.Fatalf("Error when reading input: %v", err)
	}
	return strings.ToLower(strings.Trim(strings.TrimSpace(text), "`'\""))
}

func inferText(input string, wr io.Writer) (ClaimAction, error) {
	if input == "y" || input == "yes" {
		return AcceptClaim, nil
	} else if input == "n" || input == "no" {
		return RejectClaim, nil
	} else if input == "skip" || input == "s" {
		return SkipClaim, nil
	} else {
		_, err := fmt.Fprintf(wr, "Unknown input, skipping any action.\n")
		if err != nil {
			return -1, err
		}
		return SkipClaim, nil
	}
}
