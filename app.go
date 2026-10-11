package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type runner func(name string, args ...string) error

type app struct {
	args        []string
	in          io.Reader
	out, errOut io.Writer
	unitDir     string
	user        bool
	runCmd      runner
}

func newApp(args []string, in io.Reader, out, errOut io.Writer) (*app, error) {
	a := &app{in: in, out: out, errOut: errOut}
	a.runCmd = func(name string, args ...string) error {
		cmd := exec.Command(name, args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = a.in, a.out, a.errOut
		return cmd.Run()
	}
	for len(args) > 0 && (args[0] == "--user" || args[0] == "--system") {
		a.user = args[0] == "--user"
		args = args[1:]
	}
	if a.user {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		a.unitDir = filepath.Join(home, ".config/systemd/user")
	} else {
		a.unitDir = "/etc/systemd/system"
	}
	a.args = args
	return a, nil
}

func (a *app) systemctl(args ...string) error {
	if a.user {
		args = append([]string{"--user"}, args...)
	}
	return a.runCmd("systemctl", args...)
}

func (a *app) reload() error { return a.systemctl("daemon-reload") }

func (a *app) run() error {
	if len(a.args) == 0 {
		a.help()
		return nil
	}
	cmd, args := a.args[0], a.args[1:]
	switch cmd {
	case "help", "-h", "--help":
		a.help()
		return nil
	case "new", "add", "create":
		return a.newService(args)
	case "ls", "list":
		return a.list(args)
	case "show", "cat":
		return a.oneSystemctl("cat", args)
	case "status":
		return a.oneSystemctl("status", args)
	case "start", "stop", "restart", "reload", "enable", "disable", "mask", "unmask":
		return a.oneSystemctl(cmd, args)
	case "logs":
		return a.logs(args)
	case "set", "configure":
		return a.set(args)
	case "edit":
		if len(args) == 1 {
			return a.edit(args[0])
		}
		return a.set(args)
	case "unset":
		return a.unset(args)
	case "clone", "copy":
		return a.clone(args)
	case "rm", "remove", "delete":
		return a.remove(args)
	default:
		return fmt.Errorf("unknown command %q (run 'services help')", cmd)
	}
}

func (a *app) help() {
	fmt.Fprint(a.out, `services manages systemd services without hand editing unit files.

Usage: services [--user] COMMAND [arguments]

  new NAME --exec COMMAND [options]   Create a service
  ls                                 List installed services
  show NAME                          Show effective unit configuration
  status NAME                        Show status
  edit NAME                           Guided settings editor
  set NAME KEY=VALUE...              Change settings via a drop-in
  unset NAME KEY...                  Remove settings made by this CLI
  clone SOURCE DEST                  Copy an existing service
  start|stop|restart|reload NAME     Control a service
  enable|disable|mask|unmask NAME    Control boot behavior
  logs NAME [-f] [-n COUNT]          Read the journal
  rm NAME --yes                      Stop, disable, and remove a local unit

Common set keys: exec, workdir, user, group, restart, restart-sec,
description, after, wants, wanted-by, type, env.VARIABLE.
Any systemd directive can be set as Section.Key, e.g. Service.TimeoutStartSec.
Use --user before a command to manage your user services.
`)
}

func (a *app) oneSystemctl(command string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: services %s NAME", command)
	}
	name, err := serviceName(args[0])
	if err != nil {
		return err
	}
	return a.systemctl(command, name)
}

func (a *app) list(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: services ls")
	}
	return a.systemctl("list-unit-files", "--type=service", "--no-pager")
}

func (a *app) logs(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: services logs NAME [-f] [-n COUNT]")
	}
	name, err := serviceName(args[0])
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(a.errOut)
	follow := fs.Bool("f", false, "follow logs")
	count := fs.Int("n", 50, "number of lines")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if len(fs.Args()) != 0 || *count < 0 {
		return errors.New("usage: services logs NAME [-f] [-n COUNT]")
	}
	cmd := []string{"--no-pager", "-n", strconv.Itoa(*count)}
	if a.user {
		cmd = append(cmd, "--user-unit", name)
	} else {
		cmd = append(cmd, "-u", name)
	}
	if *follow {
		cmd = append(cmd, "-f")
		cmd = removeArg(cmd, "--no-pager")
	}
	return a.runCmd("journalctl", cmd...)
}

func removeArg(args []string, value string) []string {
	result := args[:0]
	for _, arg := range args {
		if arg != value {
			result = append(result, arg)
		}
	}
	return result
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ", ") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func (a *app) newService(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: services new NAME --exec COMMAND [--start] [--enable]")
	}
	name, err := serviceName(args[0])
	if err != nil {
		return err
	}
	defaultAfter, defaultTarget := "network.target", "multi-user.target"
	if a.user {
		defaultAfter, defaultTarget = "", "default.target"
	}
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	fs.SetOutput(a.errOut)
	execStart := fs.String("exec", "", "command to run")
	description := fs.String("description", strings.TrimSuffix(name, ".service"), "description")
	workdir := fs.String("workdir", "", "working directory")
	user := fs.String("user", "", "service account")
	group := fs.String("group", "", "service group")
	restart := fs.String("restart", "on-failure", "restart policy")
	after := fs.String("after", defaultAfter, "ordering dependency")
	wantedBy := fs.String("wanted-by", defaultTarget, "enable target")
	start := fs.Bool("start", false, "start after creation")
	enable := fs.Bool("enable", false, "enable at boot")
	var envs stringList
	fs.Var(&envs, "env", "environment KEY=VALUE (repeatable)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("usage: services new NAME --exec COMMAND [--description TEXT] [--workdir DIR] [--user USER] [--group GROUP] [--env KEY=VALUE] [--restart POLICY] [--after UNIT] [--wanted-by TARGET] [--start] [--enable]")
	}
	if *execStart == "" {
		fmt.Fprintf(a.out, "Command to run for %s: ", name)
		line, err := bufio.NewReader(a.in).ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		*execStart = strings.TrimSpace(line)
		if *execStart == "" {
			return errors.New("a command is required; pass --exec COMMAND")
		}
	}
	for _, v := range []string{*execStart, *description, *workdir, *user, *group, *restart, *after, *wantedBy} {
		if err := validateValue(v); err != nil {
			return err
		}
	}
	path := filepath.Join(a.unitDir, name)
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[Unit]\nDescription=%s\n", *description)
	if *after != "" {
		fmt.Fprintf(&b, "After=%s\n", *after)
	}
	fmt.Fprintf(&b, "\n[Service]\nExecStart=%s\n", *execStart)
	if *workdir != "" {
		fmt.Fprintf(&b, "WorkingDirectory=%s\n", *workdir)
	}
	if *user != "" {
		fmt.Fprintf(&b, "User=%s\n", *user)
	}
	if *group != "" {
		fmt.Fprintf(&b, "Group=%s\n", *group)
	}
	if *restart != "" {
		fmt.Fprintf(&b, "Restart=%s\n", *restart)
	}
	for _, item := range envs {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			return fmt.Errorf("invalid --env %q; use KEY=VALUE", item)
		}
		quoted, err := quoteEnvironment(key, value)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "Environment=%s\n", quoted)
	}
	if *wantedBy != "" {
		fmt.Fprintf(&b, "\n[Install]\nWantedBy=%s\n", *wantedBy)
	}
	if err := createUnit(path, []byte(b.String())); err != nil {
		return err
	}
	if err := a.reload(); err != nil {
		return fmt.Errorf("created %s but daemon-reload failed: %w", path, err)
	}
	if *enable {
		if err := a.systemctl("enable", name); err != nil {
			return err
		}
	}
	if *start {
		if err := a.systemctl("start", name); err != nil {
			return err
		}
	}
	fmt.Fprintf(a.out, "Created %s\n", name)
	return nil
}

func (a *app) dropin(name string) string {
	return filepath.Join(a.unitDir, name+".d", "50-services.conf")
}

func (a *app) edit(rawName string) error {
	name, err := serviceName(rawName)
	if err != nil {
		return err
	}
	if err := a.ensureExists(name); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "Enter a setting such as exec, workdir, user, restart, env.KEY, or Section.Key.")
	fmt.Fprintln(a.out, "Press Enter on an empty setting to save.")
	scanner := bufio.NewScanner(a.in)
	var assignments []string
	for {
		fmt.Fprint(a.out, "Setting: ")
		if !scanner.Scan() {
			break
		}
		key := strings.TrimSpace(scanner.Text())
		if key == "" {
			break
		}
		if strings.HasPrefix(key, "env.") {
			if !envPattern.MatchString(strings.TrimPrefix(key, "env.")) {
				return fmt.Errorf("invalid environment key %q", key)
			}
		} else if _, _, err := directive(key); err != nil {
			return err
		}
		fmt.Fprint(a.out, "Value: ")
		if !scanner.Scan() {
			return errors.New("missing value")
		}
		assignments = append(assignments, key+"="+scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(assignments) == 0 {
		return nil
	}
	return a.set(append([]string{name}, assignments...))
}

func (a *app) set(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: services set NAME KEY=VALUE...")
	}
	name, err := serviceName(args[0])
	if err != nil {
		return err
	}
	path := a.dropin(name)
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	u, err := parseUnit(data)
	if err != nil {
		return err
	}
	var envUpdates []struct{ key, value string }
	for _, assignment := range args[1:] {
		key, value, ok := strings.Cut(assignment, "=")
		if !ok {
			return fmt.Errorf("invalid assignment %q; use KEY=VALUE", assignment)
		}
		if err := validateValue(value); err != nil {
			return err
		}
		if strings.HasPrefix(key, "env.") {
			envKey := strings.TrimPrefix(key, "env.")
			if _, err := quoteEnvironment(envKey, value); err != nil {
				return err
			}
			envUpdates = append(envUpdates, struct{ key, value string }{envKey, value})
			continue
		}
		section, directiveKey, err := directive(key)
		if err != nil {
			return err
		}
		u.set(section, directiveKey, value)
	}
	if err := a.ensureExists(name); err != nil {
		return err
	}
	if len(u) > 0 {
		if err := atomicWrite(path, u.render()); err != nil {
			return err
		}
	}
	for _, update := range envUpdates {
		if err := a.setEnv(name, update.key, update.value); err != nil {
			return err
		}
	}
	if err := a.reload(); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Updated %s. Run 'services restart %s' to apply runtime changes.\n", name, name)
	return nil
}

func (a *app) envPath(name, key string) string {
	return filepath.Join(a.unitDir, name+".d", "60-services-env-"+key+".conf")
}

func (a *app) setEnv(name, key, value string) error {
	quoted, err := quoteEnvironment(key, value)
	if err != nil {
		return err
	}
	return atomicWrite(a.envPath(name, key), []byte("[Service]\nEnvironment="+quoted+"\n"))
}

func (a *app) unset(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: services unset NAME KEY...")
	}
	name, err := serviceName(args[0])
	if err != nil {
		return err
	}
	path := a.dropin(name)
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	u, err := parseUnit(data)
	if err != nil {
		return err
	}
	var envPaths []string
	for _, key := range args[1:] {
		if strings.HasPrefix(key, "env.") {
			key = strings.TrimPrefix(key, "env.")
			if !envPattern.MatchString(key) {
				return fmt.Errorf("invalid environment variable %q", key)
			}
			envPaths = append(envPaths, a.envPath(name, key))
			continue
		}
		section, directiveKey, err := directive(key)
		if err != nil {
			return err
		}
		u.remove(section, directiveKey)
	}
	if err := a.ensureExists(name); err != nil {
		return err
	}
	for _, path := range envPaths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if len(u) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else if err := atomicWrite(path, u.render()); err != nil {
		return err
	}
	if err := a.reload(); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Updated %s. Run 'services restart %s' to apply runtime changes.\n", name, name)
	return nil
}

func (a *app) ensureExists(name string) error {
	var output strings.Builder
	previous := a.out
	a.out = &output
	err := a.systemctl("show", name, "--property=LoadState", "--value")
	a.out = previous
	if err != nil {
		return err
	}
	if strings.TrimSpace(output.String()) != "loaded" {
		return fmt.Errorf("service %s is not loaded", name)
	}
	return nil
}

func (a *app) clone(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: services clone SOURCE DEST")
	}
	source, err := serviceName(args[0])
	if err != nil {
		return err
	}
	dest, err := serviceName(args[1])
	if err != nil {
		return err
	}
	if source == dest {
		return errors.New("source and destination must differ")
	}
	path := filepath.Join(a.unitDir, dest)
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	var output strings.Builder
	previous := a.out
	a.out = &output
	err = a.systemctl("show", source, "--property=FragmentPath", "--value")
	a.out = previous
	if err != nil {
		return err
	}
	fragment := strings.TrimSpace(output.String())
	if fragment == "" {
		return fmt.Errorf("no unit file found for %s", source)
	}
	data, err := os.ReadFile(fragment)
	if err != nil {
		return err
	}
	output.Reset()
	previous = a.out
	a.out = &output
	err = a.systemctl("show", source, "--property=DropInPaths", "--value")
	a.out = previous
	if err != nil {
		return err
	}
	type dropin struct {
		name string
		data []byte
	}
	var dropins []dropin
	for _, sourcePath := range strings.Fields(output.String()) {
		contents, err := os.ReadFile(sourcePath)
		if err != nil {
			return err
		}
		dropins = append(dropins, dropin{filepath.Base(sourcePath), contents})
	}
	if err := createUnit(path, data); err != nil {
		return err
	}
	for _, item := range dropins {
		if err := atomicWrite(filepath.Join(a.unitDir, dest+".d", item.name), item.data); err != nil {
			return err
		}
	}
	if len(dropins) > 0 {
		var names []string
		for _, item := range dropins {
			names = append(names, item.name)
		}
		if err := atomicWrite(filepath.Join(a.unitDir, dest+".d", ".services-clone-manifest"), []byte(strings.Join(names, "\n")+"\n")); err != nil {
			return err
		}
	}
	if err := a.reload(); err != nil {
		return fmt.Errorf("cloned %s but daemon-reload failed: %w", path, err)
	}
	fmt.Fprintf(a.out, "Cloned %s to %s. Review its ExecStart and dependencies before starting it.\n", source, dest)
	return nil
}

func (a *app) remove(args []string) error {
	if len(args) != 2 || args[1] != "--yes" {
		return errors.New("usage: services rm NAME --yes")
	}
	name, err := serviceName(args[0])
	if err != nil {
		return err
	}
	path := filepath.Join(a.unitDir, name)
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to remove non-regular unit %s", path)
	}
	if err := a.systemctl("disable", "--now", name); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	dropinDir := filepath.Join(a.unitDir, name+".d")
	manifestPath := filepath.Join(dropinDir, ".services-clone-manifest")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	cloned := map[string]bool{}
	for _, item := range strings.Split(string(manifest), "\n") {
		if item != "" && filepath.Base(item) == item && strings.HasSuffix(item, ".conf") {
			cloned[item] = true
		}
	}
	entries, err := os.ReadDir(dropinDir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		owned := entry.Name() == "50-services.conf" || strings.HasPrefix(entry.Name(), "60-services-env-") && strings.HasSuffix(entry.Name(), ".conf") || cloned[entry.Name()]
		if owned && !entry.IsDir() {
			if err := os.Remove(filepath.Join(dropinDir, entry.Name())); err != nil {
				return err
			}
		}
	}
	if err := os.Remove(manifestPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = os.Remove(dropinDir)
	if err := a.reload(); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Removed %s\n", name)
	return nil
}
