package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
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
	state := &tuiState{app: a, showAll: true}
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

func TestTUIRefreshMergesLiveState(t *testing.T) {
	a, _ := testApp(t)
	a.runCmd = func(name string, args ...string) error {
		command := strings.Join(args, " ")
		switch {
		case strings.Contains(command, "list-unit-files"):
			fmt.Fprint(a.out, "api.service enabled\nworker.service disabled\n")
		case strings.Contains(command, "list-units"):
			fmt.Fprint(a.out, "api.service loaded active running API server\n● worker.service loaded failed failed Background worker\n")
		default:
			return fmt.Errorf("unexpected command: %s %v", name, args)
		}
		return nil
	}
	state := &tuiState{app: a}
	if err := state.refresh(); err != nil {
		t.Fatal(err)
	}
	if len(state.rows) != 2 || state.rows[0].name != "worker.service" || state.rows[0].runtime != "failed" || state.rows[1].description != "API server" {
		t.Fatalf("unexpected rows: %+v", state.rows)
	}
}

func TestTUIFitRemovesTerminalControls(t *testing.T) {
	if got := fit("hello\x1b[31m\nworld", 20); got != "hello world" {
		t.Fatalf("unsafe terminal text: %q", got)
	}
	if got := fit("東京 service", 6); runewidth.StringWidth(got) > 6 {
		t.Fatalf("wide text overflowed: %q", got)
	}
}

func TestTUIFocusAndSearch(t *testing.T) {
	state := &tuiState{rows: []serviceRow{
		{name: "running.service", runtime: "running", state: "disabled"},
		{name: "enabled.service", runtime: "inactive", state: "enabled"},
		{name: "disabled.service", runtime: "inactive", state: "disabled"},
	}}
	if got := len(state.visible()); got != 2 {
		t.Fatalf("focus has %d rows", got)
	}
	state.showAll = true
	if got := len(state.visible()); got != 3 {
		t.Fatalf("all has %d rows", got)
	}
	state.query = "disabled"
	if got := len(state.visible()); got != 1 {
		t.Fatalf("search has %d rows", got)
	}
	state.ensureSelection()
	if got := state.name(); got != "disabled.service" {
		t.Fatalf("selected %q", got)
	}
}

func TestTUIScreenFitsSmallTerminal(t *testing.T) {
	state := &tuiState{app: &app{}, rows: []serviceRow{{name: "a-very-long-service-name.service", runtime: "running", state: "enabled", description: "東京のサービス · a long description"}}}
	for _, size := range [][2]int{{30, 10}, {55, 14}, {100, 30}} {
		screen := state.screen(size[0], size[1])
		lines := strings.Split(screen, "\r\n")
		if len(lines) != size[1] {
			t.Fatalf("%v: got %d lines", size, len(lines))
		}
		for _, line := range lines {
			plain := clean(line)
			if runewidth.StringWidth(plain) >= size[0] {
				t.Fatalf("%v: line too long: %q", size, plain)
			}
		}
	}
}
