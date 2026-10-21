package coreservices_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// runSample runs the page's sample, core-services/main.go, and returns its
// standard output lines. Start in the sample binds no port.
func runSample(t *testing.T) []string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", "run", "./core-services")
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go run ./core-services: %v\n%s", err, out)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// Docs: core/services.mdx#register-under-the-type-consumers-ask-for
// "Because lookups are by exact type, a value registered under its concrete type is
// not found by a constructor that asks for an interface it implements"
// Docs: core/services.mdx#register-under-the-type-consumers-ask-for
// "building *main.Signup: resolving dep main.Mailer for *main.Signup: no registration for main.Mailer"
// Docs: core/services.mdx#register-under-the-type-consumers-ask-for
// "Register it under the interface instead"
// Docs: core/services.mdx#resolving
// "if _, err := c.Resolve[*Nonce](); errors.Is(err, gas.ErrNotRegistered) {"
// The sample prints mismatch()'s error, whether it wraps ErrNotRegistered,
// and fixed()'s error.
func TestSampleOutput(t *testing.T) {
	t.Parallel()
	lines := runSample(t)
	want := []string{
		"no *Nonce registered on this app",
		"notes 0",
		"building *main.Signup: resolving dep main.Mailer for *main.Signup: no registration for main.Mailer",
		"true",
		"<nil>",
	}
	if !slices.Equal(lines, want) {
		t.Fatalf("sample output:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}
