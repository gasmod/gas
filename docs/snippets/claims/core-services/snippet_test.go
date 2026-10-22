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
// "Here `Signup` asks for the `Mailer` interface, so `SMTPMailer` is
// registered under `Mailer`"
// Docs: core/services.mdx#register-under-the-type-consumers-ask-for
// "a constructor
// that asks for an interface does not find a value registered under a concrete
// type that implements it, and the build fails"
// Docs: core/services.mdx#register-under-the-type-consumers-ask-for
// "building *main.Signup: resolving dep main.Mailer for *main.Signup: no registration for main.Mailer"
// Docs: core/services.mdx#resolving
// "if _, err := c.Resolve[*Nonce](); errors.Is(err, gas.ErrNotRegistered) {"
// Docs: core/services.mdx#overriding-a-registration
// "Registering a type again replaces its earlier registration: the last one
// wins."
// The sample prints underInterface()'s error, underConcrete()'s error and
// whether it wraps ErrNotRegistered, and the Mailer a worker built from
// testOptions() resolves.
func TestSampleOutput(t *testing.T) {
	t.Parallel()
	lines := runSample(t)
	want := []string{
		"no *Nonce registered on this app",
		"notes 0",
		"<nil>",
		"building *main.Signup: resolving dep main.Mailer for *main.Signup: no registration for main.Mailer",
		"true",
		"*main.FakeMailer",
	}
	if !slices.Equal(lines, want) {
		t.Fatalf("sample output:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}
