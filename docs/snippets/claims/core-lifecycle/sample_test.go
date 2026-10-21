package corelifecycle_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// Docs: core/lifecycle.mdx#the-whole-sequence
// "GET /readyz: 204 / init db / init cache / event SystemAllServicesInitialized / ready hook / event AppConfigUpdated / event SystemServerShuttingDown / event SystemShuttingDown / close cache / close db"
// Docs: core/lifecycle.mdx#startup-order
// "Running the [lifecycle sample](#the-whole-sequence) prints the order directly."
// The sample's log lines go to standard error; its standard output is the
// sequence the page shows.
func TestSampleOutput(t *testing.T) {
	t.Parallel()
	cmd := exec.CommandContext(t.Context(), "go", "run", "./core-lifecycle")
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go run ./core-lifecycle: %v\n%s", err, out)
	}
	got := strings.Split(strings.TrimSpace(string(out)), "\n")
	want := []string{
		"GET /readyz: 204",
		"init db",
		"init cache",
		"event SystemAllServicesInitialized",
		"ready hook",
		"event AppConfigUpdated",
		"event SystemServerShuttingDown",
		"event SystemShuttingDown",
		"close cache",
		"close db",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sample output:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
