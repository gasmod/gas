package conceptsownership_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "go.work")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			t.Fatal("go.work not found")
		}
		d = filepath.Dir(d)
	}
}

const payloadPrelude = `package main

import "github.com/gasmod/gas"

type UserCreatedPayload struct{ Email string }

type OtherPayload struct{ N int }

type UserCreated struct{ gas.Event[UserCreatedPayload] }

func main() {
	bus := gas.NewEventBus()
	_ = bus
`

// Docs: concepts/ownership.mdx#events
// "Embedding `gas.Event[Payload]` binds the event to its payload, so a handler or an emit with the wrong payload does not compile"
// Builds small programs in a temporary module that replaces
// github.com/gasmod/gas with this repository, outside the workspace.
func TestWrongPayloadDoesNotCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("builds programs with the go tool")
	}
	t.Parallel()
	root := repoRoot(t)
	sum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	mod := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(mod, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/payloadcheck\n\ngo 1.27.3\n\nrequire github.com/gasmod/gas v0.6.0\n\nreplace github.com/gasmod/gas => "+root+"\n")
	write("go.sum", string(sum))
	programs := map[string]string{
		"ok":           "\tbus.Subscribe[UserCreated](func(UserCreatedPayload) {})\n\tbus.Emit[UserCreated](UserCreatedPayload{}).Wait()\n",
		"wrongemit":    "\tbus.Emit[UserCreated](OtherPayload{}).Wait()\n",
		"wronghandler": "\tbus.Subscribe[UserCreated](func(OtherPayload) {})\n",
	}
	for dir, body := range programs {
		write(filepath.Join(dir, "main.go"), payloadPrelude+body+"}\n")
	}
	build := func(dir string) (string, error) {
		cmd := exec.CommandContext(t.Context(), "go", "build", "-o", os.DevNull, "./"+dir)
		cmd.Dir = mod
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "GOPROXY=off")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := build("ok"); err != nil {
		t.Fatalf("the correct payload does not build, so the harness is broken: %v\n%s", err, out)
	}
	for _, dir := range []string{"wrongemit", "wronghandler"} {
		out, err := build(dir)
		if err == nil {
			t.Errorf("%s compiled with the wrong payload", dir)
			continue
		}
		// A type error at the call site names the event's payload binding;
		// anything else (module resolution, a typo) is a broken harness.
		if !strings.Contains(out, dir+"/main.go:") || !strings.Contains(out, "UserCreated") {
			t.Errorf("%s failed for another reason than the payload type:\n%s", dir, out)
		}
	}
}
