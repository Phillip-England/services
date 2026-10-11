package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testApp(t *testing.T, args ...string) (*app, *[]string) {
	t.Helper()
	var out, errOut bytes.Buffer
	a, err := newApp(args, strings.NewReader(""), &out, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	a.unitDir = t.TempDir()
	commands := []string{}
	a.runCmd = func(name string, args ...string) error {
		commands = append(commands, strings.Join(append([]string{name}, args...), " "))
		if name == "systemctl" && len(args) > 0 && args[0] == "show" {
			if args[len(args)-1] == "--property=LoadState" || args[len(args)-2] == "--property=LoadState" { // kept for flexible argument order
				_, _ = a.out.Write([]byte("loaded\n"))
			}
		}
		return nil
	}
	return a, &commands
}

func TestNewAndSet(t *testing.T) {
	a, commands := testApp(t, "new", "worker", "--exec", "/opt/worker --serve", "--env", "TOKEN=a b%", "--enable", "--start")
	if err := a.run(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(a.unitDir, "worker.service"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "ExecStart=/opt/worker --serve") || !strings.Contains(string(data), `Environment="TOKEN=a b%%"`) {
		t.Fatalf("unexpected unit:\n%s", data)
	}
	want := []string{"systemctl daemon-reload", "systemctl enable worker.service", "systemctl start worker.service"}
	if !reflect.DeepEqual(*commands, want) {
		t.Fatalf("commands = %q, want %q", *commands, want)
	}
	a.args = []string{"set", "worker", "exec=/opt/worker --new", "restart=always", "env.TOKEN=secret"}
	if err := a.run(); err != nil {
		t.Fatal(err)
	}
	override, err := os.ReadFile(a.dropin("worker.service"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(override), "ExecStart=\nExecStart=/opt/worker --new") || !strings.Contains(string(override), "Restart=always") {
		t.Fatalf("unexpected override:\n%s", override)
	}
	env, err := os.ReadFile(a.envPath("worker.service", "TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	if string(env) != "[Service]\nEnvironment=\"TOKEN=secret\"\n" {
		t.Fatalf("unexpected env override: %s", env)
	}
	a.args = []string{"unset", "worker", "exec", "env.TOKEN"}
	if err := a.run(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.envPath("worker.service", "TOKEN")); !os.IsNotExist(err) {
		t.Fatalf("env override still exists: %v", err)
	}
	override, err = os.ReadFile(a.dropin("worker.service"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(override), "ExecStart") || !strings.Contains(string(override), "Restart=always") {
		t.Fatalf("unexpected override:\n%s", override)
	}
}

func TestSetRejectsInvalidInputWithoutChanges(t *testing.T) {
	a, commands := testApp(t, "set", "worker", "env.OK=yes", "Service.ExecStart=bad\ncommand")
	if err := a.run(); err == nil {
		t.Fatal("expected validation error")
	}
	if len(*commands) != 0 {
		t.Fatalf("unexpected commands: %v", *commands)
	}
	if _, err := os.Stat(a.envPath("worker.service", "OK")); !os.IsNotExist(err) {
		t.Fatalf("created env file: %v", err)
	}
}

func TestRemovePreservesOtherDropins(t *testing.T) {
	a, commands := testApp(t, "rm", "worker", "--yes")
	if err := os.WriteFile(filepath.Join(a.unitDir, "worker.service"), []byte("[Service]\nExecStart=/bin/true\n"), 0644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(a.unitDir, "worker.service.d")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"50-services.conf", "other.conf"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("[Service]\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.run(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "other.conf")); err != nil {
		t.Fatalf("unowned drop-in removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "50-services.conf")); !os.IsNotExist(err) {
		t.Fatalf("owned drop-in remains: %v", err)
	}
	if len(*commands) != 2 || (*commands)[0] != "systemctl disable --now worker.service" {
		t.Fatalf("commands: %v", *commands)
	}
}

func TestNameValidation(t *testing.T) {
	for _, name := range []string{"../bad", "/tmp/bad", "bad name", ""} {
		if _, err := serviceName(name); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	if name, err := serviceName("worker"); err != nil || name != "worker.service" {
		t.Fatalf("got %q, %v", name, err)
	}
}
