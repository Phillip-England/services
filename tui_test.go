package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestTUIRefreshPreservesSelection(t *testing.T) {
	a, _ := testApp(t)
	a.runCmd = func(name string, args ...string) error {
		if name != "systemctl" || !strings.Contains(strings.Join(args, " "), "list-unit-files") {
			return fmt.Errorf("unexpected command: %s %v", name, args)
		}
		fmt.Fprint(a.out, "alpha.service enabled\nbeta.service disabled\n2 unit files listed.\n")
		return nil
	}
	state := &tuiState{app: a}
	if err := state.refresh(); err != nil {
		t.Fatal(err)
	}
	if len(state.rows) != 2 || state.rows[1].name != "beta.service" {
		t.Fatalf("unexpected rows: %+v", state.rows)
	}
	state.selected = 1
	if err := state.refresh(); err != nil {
		t.Fatal(err)
	}
	if state.name() != "beta.service" {
		t.Fatalf("selection changed to %q", state.name())
	}
}

func TestTUIFitRemovesTerminalControls(t *testing.T) {
	if got := fit("hello\x1b[31m\nworld", 10); strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\n') {
		t.Fatalf("unsafe terminal text: %q", got)
	}
}
