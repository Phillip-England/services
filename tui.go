package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"unicode"

	"golang.org/x/term"
)

var errTUIQuit = errors.New("quit TUI")

type serviceRow struct {
	name, state string
}

type tuiState struct {
	app      *app
	tty      *os.File
	keys     *bufio.Reader
	rows     []serviceRow
	selected int
	message  string
}

func (a *app) tui(args []string) error {
	if len(args) > 1 {
		return errors.New("usage: services [--user] tui [SUDO_PASSWORD]")
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil || !term.IsTerminal(int(tty.Fd())) {
		if tty != nil {
			tty.Close()
		}
		return errors.New("tui requires an interactive terminal")
	}
	defer tty.Close()
	if !a.user && os.Geteuid() != 0 {
		if len(args) != 1 || args[0] == "" {
			return errors.New("system TUI requires a sudo password: services tui 'SUDO_PASSWORD'")
		}
		return launchElevatedTUI(tty, args[0])
	}
	old, err := term.MakeRaw(int(tty.Fd()))
	if err != nil {
		return err
	}
	defer term.Restore(int(tty.Fd()), old)
	fmt.Fprint(tty, "\x1b[?1049h\x1b[?25l")
	defer fmt.Fprint(tty, "\x1b[?25h\x1b[?1049l")
	t := &tuiState{app: a, tty: tty, keys: bufio.NewReader(tty)}
	if err := t.refresh(); err != nil {
		t.message = err.Error()
	}
	return t.loop()
}

func launchElevatedTUI(tty *os.File, password string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command("sudo", "-k", "-S", "-p", "", "--", exe, "tui")
	cmd.Stdin = strings.NewReader(password + "\n")
	cmd.Stdout, cmd.Stderr = tty, tty
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("could not start elevated TUI (check the sudo password): %w", err)
	}
	return nil
}

func (a *app) capture(fn func() error) (string, error) {
	var output, errorsOut bytes.Buffer
	oldOut, oldErr := a.out, a.errOut
	a.out, a.errOut = &output, &errorsOut
	defer func() { a.out, a.errOut = oldOut, oldErr }()
	err := fn()
	if err != nil && errorsOut.Len() != 0 {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(errorsOut.String()))
	}
	return output.String(), err
}

func (t *tuiState) command(args ...string) (string, error) {
	old := t.app.args
	t.app.args = args
	defer func() { t.app.args = old }()
	return t.app.capture(t.app.run)
}

func (t *tuiState) refresh() error {
	selected := t.name()
	output, err := t.app.capture(func() error {
		return t.app.systemctl("list-unit-files", "--type=service", "--no-legend", "--no-pager", "--plain")
	})
	if err != nil {
		return err
	}
	t.rows = nil
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.HasSuffix(fields[0], ".service") {
			t.rows = append(t.rows, serviceRow{fields[0], fields[1]})
		}
	}
	if selected != "" {
		for i, row := range t.rows {
			if row.name == selected {
				t.selected = i
				return nil
			}
		}
	}
	if t.selected >= len(t.rows) {
		t.selected = max(0, len(t.rows)-1)
	}
	return nil
}

func (t *tuiState) name() string {
	if t.selected < 0 || t.selected >= len(t.rows) {
		return ""
	}
	return t.rows[t.selected].name
}

func (t *tuiState) size() (int, int) {
	w, h, err := term.GetSize(int(t.tty.Fd()))
	if err != nil || w < 30 || h < 10 {
		return 80, 24
	}
	return w, h
}

func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || (unicode.IsPrint(r) && r != '\x1b') {
			return r
		}
		return -1
	}, s)
}

func fit(s string, width int) string {
	s = strings.NewReplacer("\n", " ", "\t", " ").Replace(clean(s))
	r := []rune(s)
	if len(r) > width {
		if width > 1 {
			return string(r[:width-1]) + "…"
		}
		return string(r[:width])
	}
	return s
}

func (t *tuiState) draw() {
	w, h := t.size()
	fmt.Fprint(t.tty, "\x1b[H\x1b[2J")
	scope := "system"
	if t.app.user {
		scope = "user"
	}
	fmt.Fprintln(t.tty, fit(" SERVICES  ("+scope+")  ↑/↓ select  Enter status  ? help  q quit", w))
	fmt.Fprintln(t.tty, strings.Repeat("─", min(w, 80)))
	visible := h - 6
	start := 0
	if t.selected >= visible {
		start = t.selected - visible + 1
	}
	for i := 0; i < visible; i++ {
		index := start + i
		if index < len(t.rows) {
			row := t.rows[index]
			prefix := "  "
			if index == t.selected {
				prefix = "▶ "
			}
			fmt.Fprintln(t.tty, fit(fmt.Sprintf("%s%-48s %s", prefix, row.name, row.state), w))
		} else {
			fmt.Fprintln(t.tty)
		}
	}
	fmt.Fprintln(t.tty, strings.Repeat("─", min(w, 80)))
	fmt.Fprintln(t.tty, fit("n new  e edit  u unset  c clone  s start  t stop  r restart  b boot  m mask", w))
	fmt.Fprintln(t.tty, fit("v unit  l logs  x remove  R refresh", w))
	fmt.Fprint(t.tty, fit(t.message, w))
}

func (t *tuiState) key() (rune, error) {
	r, _, err := t.keys.ReadRune()
	if err != nil {
		return 0, err
	}
	if r == 27 {
		// Arrow keys arrive as ESC [ A/B. Escape alone is ignored by the UI.
		if t.keys.Buffered() >= 2 {
			first, _, _ := t.keys.ReadRune()
			second, _, _ := t.keys.ReadRune()
			if first == '[' {
				if second == 'A' {
					return 'k', nil
				}
				if second == 'B' {
					return 'j', nil
				}
			}
		}
		return 0, nil
	}
	if r == 3 {
		return 0, errTUIQuit
	}
	return r, nil
}

func (t *tuiState) prompt(label string) (string, error) {
	w, h := t.size()
	var input []rune
	for {
		fmt.Fprintf(t.tty, "\x1b[%d;1H\x1b[2K%s\x1b[?25h", h, fit(label+string(input), w))
		r, err := t.key()
		if err != nil {
			return "", err
		}
		switch r {
		case '\r', '\n':
			fmt.Fprint(t.tty, "\x1b[?25l")
			return string(input), nil
		case 127, 8:
			if len(input) > 0 {
				input = input[:len(input)-1]
			}
		default:
			if unicode.IsPrint(r) {
				input = append(input, r)
			}
		}
	}
}

func (t *tuiState) confirm(warning string) (bool, error) {
	answer, err := t.prompt(warning + " [y/N]: ")
	return strings.EqualFold(strings.TrimSpace(answer), "y"), err
}

func (t *tuiState) view(title, content string) error {
	lines := strings.Split(clean(content), "\n")
	offset := 0
	for {
		w, h := t.size()
		fmt.Fprint(t.tty, "\x1b[H\x1b[2J")
		fmt.Fprintln(t.tty, fit(title+"  ↑/↓ scroll  q/Enter back", w))
		fmt.Fprintln(t.tty, strings.Repeat("─", min(w, 80)))
		for i := 0; i < h-3; i++ {
			if offset+i < len(lines) {
				fmt.Fprintln(t.tty, fit(lines[offset+i], w))
			} else {
				fmt.Fprintln(t.tty)
			}
		}
		key, err := t.key()
		if err != nil {
			return err
		}
		switch key {
		case 'q', '\r', '\n':
			return nil
		case 'j':
			if offset < max(0, len(lines)-(h-3)) {
				offset++
			}
		case 'k':
			if offset > 0 {
				offset--
			}
		}
	}
}

func (t *tuiState) apply(args ...string) {
	output, err := t.command(args...)
	if err != nil {
		t.message = clean(err.Error())
	} else {
		t.message = clean(strings.TrimSpace(output))
		if t.message == "" {
			t.message = strings.Join(args, " ") + " complete"
		}
	}
	if refreshErr := t.refresh(); refreshErr != nil && err == nil {
		t.message = refreshErr.Error()
	}
}

func (t *tuiState) loop() error {
	for {
		t.draw()
		key, err := t.key()
		if errors.Is(err, errTUIQuit) {
			return nil
		}
		if err != nil {
			return err
		}
		if key == 'q' {
			return nil
		}
		if key == 'j' && t.selected+1 < len(t.rows) {
			t.selected++
			continue
		}
		if key == 'k' && t.selected > 0 {
			t.selected--
			continue
		}
		if key == 'R' {
			if err := t.refresh(); err != nil {
				t.message = err.Error()
			} else {
				t.message = "Refreshed"
			}
			continue
		}
		if key == '?' {
			if err := t.view("Help", "Select a service with ↑/↓ or j/k.\nEnter: status   v: unit file   l: recent logs\nn: create   e: set a setting   u: unset a setting   c: clone\ns: start   t: stop   r: restart\nb: enable or disable at boot   m: mask or unmask\nx: remove a local unit   R: refresh   q: quit\n\nSettings use aliases such as exec, workdir, restart, and env.NAME,\nor Section.Key such as Service.TimeoutStartSec."); err != nil && !errors.Is(err, errTUIQuit) {
				return err
			}
			continue
		}
		if err := t.action(key); errors.Is(err, errTUIQuit) {
			return nil
		} else if err != nil {
			t.message = clean(err.Error())
		}
	}
}

func (t *tuiState) action(key rune) error {
	name := t.name()
	if key == 'n' {
		newName, err := t.prompt("New service name (blank cancels): ")
		if err != nil || newName == "" {
			return err
		}
		command, err := t.prompt("ExecStart command: ")
		if err != nil || command == "" {
			return err
		}
		args := []string{"new", newName, "--exec", command}
		if description, err := t.prompt("Description (optional): "); err != nil {
			return err
		} else if description != "" {
			args = append(args, "--description", description)
		}
		if workdir, err := t.prompt("Working directory (optional): "); err != nil {
			return err
		} else if workdir != "" {
			args = append(args, "--workdir", workdir)
		}
		if user, err := t.prompt("Run as user (optional): "); err != nil {
			return err
		} else if user != "" {
			args = append(args, "--user", user)
		}
		if ok, err := t.confirm("Create " + newName + " with ExecStart=" + command + "?"); err != nil {
			return err
		} else if ok {
			t.apply(args...)
		}
		return nil
	}
	if name == "" {
		return nil
	}
	switch key {
	case '\r', '\n', 'v', 'l':
		var args []string
		switch key {
		case '\r', '\n':
			args = []string{"status", name}
		case 'v':
			args = []string{"show", name}
		case 'l':
			args = []string{"logs", name, "-n", "100"}
		}
		output, err := t.command(args...)
		if err != nil {
			t.message = err.Error()
			return nil
		}
		return t.view(strings.Join(args, " "), output)
	case 'e', 'u':
		setting, err := t.prompt("Setting key (blank cancels): ")
		if err != nil || setting == "" {
			return err
		}
		if key == 'u' {
			if ok, err := t.confirm("Remove override " + setting + " from " + name + "?"); err != nil {
				return err
			} else if ok {
				t.apply("unset", name, setting)
			}
			return nil
		}
		value, err := t.prompt("Value: ")
		if err != nil {
			return err
		}
		if ok, err := t.confirm("Set " + setting + " on " + name + "?"); err != nil {
			return err
		} else if ok {
			t.apply("set", name, setting+"="+value)
		}
	case 'c':
		dest, err := t.prompt("Clone to name (blank cancels): ")
		if err != nil || dest == "" {
			return err
		}
		if ok, err := t.confirm("Clone " + name + " to " + dest + "? Review before starting"); err != nil {
			return err
		} else if ok {
			t.apply("clone", name, dest)
		}
	case 's':
		if ok, err := t.confirm("Warning: start runs the commands in " + name + ". Continue?"); err != nil {
			return err
		} else if ok {
			t.apply("start", name)
		}
	case 't', 'r':
		action := "stop"
		if key == 'r' {
			action = "restart"
		}
		if ok, err := t.confirm("Warning: " + action + " interrupts " + name + ". Continue?"); err != nil {
			return err
		} else if ok {
			t.apply(action, name)
		}
	case 'b', 'm':
		action := "enable"
		if key == 'm' {
			action = "mask"
		} else if strings.HasPrefix(t.rows[t.selected].state, "enabled") {
			action = "disable"
		}
		if key == 'm' && strings.HasPrefix(t.rows[t.selected].state, "masked") {
			action = "unmask"
		}
		if ok, err := t.confirm("Warning: " + action + " changes boot behavior for " + name + ". Continue?"); err != nil {
			return err
		} else if ok {
			t.apply(action, name)
		}
	case 'x':
		if ok, err := t.confirm("Warning: stop, disable and DELETE local unit " + name + "?"); err != nil {
			return err
		} else if ok {
			t.apply("rm", name, "--yes")
		}
	}
	return nil
}
