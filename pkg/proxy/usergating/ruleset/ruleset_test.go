package ruleset

import (
	"testing"
)

const (
	doc = `
denied_user_names:
  - "@hack.er$"
  - "hacker@doesnoevil.com"

allowed_user_names:
  - "@doesnoevil.com$"
  - "^user@example.com$"
  - "^user@hack.er"
`
)

func TestRuleSet(t *testing.T) {
	rules, err := FromYAML([]byte(doc))

	if err != nil {
		t.Error(err)
	}

	// should allow: not denied and username is exact match
	if !rules.UserNameIsAllowed("user@example.com") {
		t.Fail()
	}

	// should deny: user name is not explicitly allowed
	if rules.UserNameIsAllowed("user@example.com.foo") {
		t.Fail()
	}

	// should deny: user name is not explicitly allowed
	if rules.UserNameIsAllowed("some+user@example.com") {
		t.Fail()
	}

	// should deny: explicit denials take precedence even if user is explicitly allowed
	if rules.UserNameIsAllowed("user@hack.er") {
		t.Fail()
	}

	// should allow: user matches allow rule and does not match any deny rule
	if !rules.UserNameIsAllowed("you@doesnoevil.com") {
		t.Fail()
	}

	// should deny: user matches a deny rule
	if rules.UserNameIsAllowed("hacker@doesnoevil.com") {
		t.Fail()
	}
}
