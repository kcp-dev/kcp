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

// ClaimAction captures the user preference on action with permission claim.
// commands.
type ClaimAction int

const (
	// User has decided to accept claim.
	AcceptClaim ClaimAction = iota
	// User has decided to reject claim.
	RejectClaim
	// User has decided to skip claim.
	SkipClaim
)

func printMessage(bindingName, claimGroup, claimResource string) string {
	return fmt.Sprintf("Accept %s-%s (APIBinding: %s) >\n", claimGroup, claimResource, bindingName)
}

func getRequiredInput(rd io.Reader, bindingName, claimGroup, claimResource string) ClaimAction {
	reader := bufio.NewReader(rd)

	for {
		printMessage(bindingName, claimGroup, claimResource)
		value := readInput(reader)
		if value != "" {
			return inferText(value)
		}
		fmt.Printf("Input is required. ")
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

func inferText(input string) ClaimAction {
	if input == "y" || input == "yes" {
		return AcceptClaim
	} else if input == "n" || input == "no" {
		return RejectClaim
	} else if input == "skip" || input == "s" {
		return SkipClaim
	} else {
		fmt.Println("Unknown input, skipping any action")
		return SkipClaim
	}
}
