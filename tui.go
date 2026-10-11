package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"unicode"

	"github.com/mattn/go-runewidth"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

var errTUIQuit = errors.New("quit TUI")
var errTUICancel = errors.New("cancelled")

type serviceRow struct {
	name, state, runtime, description string
}

type tuiState struct {
	app      *app
	tty      *os.File
	keys     *bufio.Reader
	rows     []serviceRow
	selected int
	message  string
	query    string
	showAll  bool
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
	defer fmt.Fprint(tty, "\x1b[0m\x1b[?25h\x1b[?1049l")
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
	byName := map[string]int{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.HasSuffix(fields[0], ".service") {
			byName[fields[0]] = len(t.rows)
			t.rows = append(t.rows, serviceRow{name: fields[0], state: fields[1], runtime: "inactive"})
		}
	}
	// list-unit-files supplies boot state; list-units supplies live state and description.
	// A failed live-state query should not make the entire browser unusable.
	live, liveErr := t.app.capture(func() error {
		return t.app.systemctl("list-units", "--all", "--type=service", "--no-legend", "--no-pager", "--plain")
	})
	if liveErr == nil {
		for _, line := range strings.Split(live, "\n") {
			fields := strings.Fields(line)
			if len(fields) > 0 && fields[0] == "●" {
				fields = fields[1:]
			}
			if len(fields) < 4 || !strings.HasSuffix(fields[0], ".service") {
				continue
			}
			if i, ok := byName[fields[0]]; ok {
				t.rows[i].runtime = fields[2]
				if len(fields) > 4 {
					t.rows[i].description = strings.Join(fields[4:], " ")
				}
			}
		}
	}
	sort.Slice(t.rows, func(i, j int) bool {
		a, b := t.rows[i], t.rows[j]
		priority := func(row serviceRow) int {
			if row.runtime == "failed" {
				return 0
			}
			if row.runtime == "running" {
				return 1
			}
			if strings.HasPrefix(row.state, "enabled") {
				return 2
			}
			return 3
		}
		if priority(a) != priority(b) {
			return priority(a) < priority(b)
		}
		return a.name < b.name
	})
	if selected != "" {
		for i, row := range t.rows {
			if row.name == selected {
				t.selected = i
				t.ensureSelection()
				return nil
			}
		}
	}
	if t.selected >= len(t.rows) {
		t.selected = max(0, len(t.rows)-1)
	}
	t.ensureSelection()
	return nil
}

func (t *tuiState) visible() []int {
	var result []int
	for i, row := range t.rows {
		if !t.showAll && row.runtime != "running" && row.runtime != "failed" && !strings.HasPrefix(row.state, "enabled") {
			continue
		}
		if t.query != "" && !strings.Contains(strings.ToLower(row.name+" "+row.description), strings.ToLower(t.query)) {
			continue
		}
		result = append(result, i)
	}
	return result
}

func (t *tuiState) ensureSelection() {
	for _, i := range t.visible() {
		if i == t.selected {
			return
		}
	}
	if rows := t.visible(); len(rows) > 0 {
		t.selected = rows[0]
	}
}

func (t *tuiState) move(delta int) {
	rows := t.visible()
	for n, i := range rows {
		if i == t.selected {
			t.selected = rows[max(0, min(len(rows)-1, n+delta))]
			return
		}
	}
	if len(rows) > 0 {
		t.selected = rows[0]
	}
}

func (t *tuiState) name() string {
	if t.selected < 0 || t.selected >= len(t.rows) {
		return ""
	}
	for _, i := range t.visible() {
		if i == t.selected {
			return t.rows[i].name
		}
	}
	return ""
}

func (t *tuiState) size() (int, int) {
	w, h, err := term.GetSize(int(t.tty.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		return 80, 24
	}
	return w, h
}

func clean(s string) string {
	var b strings.Builder
	escape := 0
	for _, r := range s {
		switch escape {
		case 1:
			if r == '[' {
				escape = 2
			} else if r == ']' {
				escape = 3
			} else {
				escape = 0
			}
			continue
		case 2:
			if r >= '@' && r <= '~' {
				escape = 0
			}
			continue
		case 3:
			if r == '\a' {
				escape = 0
			} else if r == '\x1b' {
				escape = 4
			}
			continue
		case 4:
			if r == '\\' {
				escape = 0
			} else {
				escape = 3
			}
			continue
		}
		if r == '\x1b' {
			escape = 1
			continue
		}
		if r == '\n' || r == '\t' || unicode.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func fit(s string, width int) string {
	s = strings.NewReplacer("\n", " ", "\t", " ").Replace(clean(s))
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= width {
		return s
	}
	limit := width - 1
	if width == 1 {
		limit = 1
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		cells := runewidth.RuneWidth(r)
		if used+cells > limit {
			break
		}
		b.WriteRune(r)
		used += cells
	}
	if width == 1 {
		return b.String()
	}
	return b.String() + "…"
}

func (t *tuiState) screen(w, h int) string {
	if w < 2 || h < 1 {
		return ""
	}
	usable := w - 1 // Avoid writing in the last column, which can auto-wrap in PTYs.
	rows := t.visible()
	position := 0
	for i, index := range rows {
		if index == t.selected {
			position = i
			break
		}
	}
	running, failed := 0, 0
	for _, row := range t.rows {
		if row.runtime == "running" {
			running++
		}
		if row.runtime == "failed" {
			failed++
		}
	}
	scope := "SYSTEM"
	if t.app.user {
		scope = "USER"
	}
	mode := "FOCUS"
	if t.showAll {
		mode = "ALL"
	}
	header := fmt.Sprintf(" SERVICES   %s  /  %s", scope, mode)
	if w >= 58 {
		header += fmt.Sprintf("                         %d running  ·  %d failed", running, failed)
	}
	subtitle := fmt.Sprintf(" %d services", len(rows))
	if t.query != "" {
		subtitle += "  /  " + t.query
	}
	if !t.showAll && t.query == "" {
		subtitle += "  ·  running, failed, or enabled"
	}
	lines := []string{header, subtitle, " " + strings.Repeat("─", max(0, usable-2))}
	nameWidth := max(10, usable-34)
	if w >= 80 {
		nameWidth = min(38, max(22, usable/3))
	}
	if h >= 7 {
		if w >= 67 {
			lines = append(lines, fmt.Sprintf("  %-*s %-10s %-10s %s", nameWidth, "SERVICE", "LIVE", "BOOT", "DESCRIPTION"))
		} else if w >= 48 {
			lines = append(lines, fmt.Sprintf("  %-*s %-10s %s", max(10, usable-27), "SERVICE", "LIVE", "BOOT"))
		} else {
			lines = append(lines, "  SERVICE                     LIVE")
		}
	}
	footerRows := 2
	available := max(0, h-len(lines)-footerRows)
	start := max(0, position-available+1)
	if available > 0 && position < available {
		start = 0
	}
	for i := 0; i < available; i++ {
		if start+i >= len(rows) {
			lines = append(lines, "")
			continue
		}
		index := rows[start+i]
		row := t.rows[index]
		marker := "  "
		if index == t.selected {
			marker = "› "
		}
		status := row.runtime
		if status == "" {
			status = "inactive"
		}
		var line string
		if w >= 67 {
			line = fmt.Sprintf("%s%-*s %-10s %-10s %s", marker, nameWidth, fit(row.name, nameWidth), fit(status, 10), fit(row.state, 10), row.description)
		} else if w >= 48 {
			nw := max(10, usable-27)
			line = fmt.Sprintf("%s%-*s %-10s %s", marker, nw, fit(row.name, nw), fit(status, 10), row.state)
		} else {
			nw := max(4, usable-13)
			line = fmt.Sprintf("%s%-*s %s", marker, nw, fit(row.name, nw), status)
		}
		lines = append(lines, line)
	}
	if len(rows) == 0 && available > 0 {
		message := "No matching services. Press Tab for all or / to search."
		if t.showAll {
			message = "No matching services. Press / to change the search."
		}
		lines[len(lines)-available] = "  " + message
	}
	if h >= 2 {
		lines = append(lines, " "+strings.Repeat("─", max(0, usable-2)))
		footer := " ↑↓ move   Enter status   / search   Tab scope   ? help   q quit"
		if t.message != "" {
			footer = " " + t.message
		}
		lines = append(lines, footer)
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	var b strings.Builder
	b.WriteString("\x1b[H")
	for i, line := range lines {
		if i > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString("\x1b[2K")
		if i == 0 {
			b.WriteString("\x1b[1;36m")
		}
		bodyStart := 3
		if h >= 7 {
			bodyStart = 4
		}
		if i >= bodyStart && i < bodyStart+available && start+i-bodyStart < len(rows) {
			row := t.rows[rows[start+i-bodyStart]]
			if rows[start+i-bodyStart] == t.selected {
				b.WriteString("\x1b[1;7m")
			} else if row.runtime == "failed" {
				b.WriteString("\x1b[31m")
			} else if row.runtime == "running" {
				b.WriteString("\x1b[32m")
			} else {
				b.WriteString("\x1b[2m")
			}
		}
		b.WriteString(fit(line, usable))
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

func (t *tuiState) draw() {
	w, h := t.size()
	fmt.Fprint(t.tty, t.screen(w, h))
}

func (t *tuiState) key() (rune, error) {
	if t.keys.Buffered() == 0 {
		fds := []unix.PollFd{{Fd: int32(t.tty.Fd()), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 250)
		if err != nil {
			if err == unix.EINTR {
				return 0, nil
			}
			return 0, err
		}
		if n == 0 {
			return 0, nil
		}
	}
	r, _, err := t.keys.ReadRune()
	if err != nil {
		return 0, err
	}
	if r == 27 {
		// SSH can deliver each byte separately. Give the rest of an escape
		// sequence a short window to arrive before treating Escape as cancel.
		if t.keys.Buffered() == 0 {
			fds := []unix.PollFd{{Fd: int32(t.tty.Fd()), Events: unix.POLLIN}}
			if n, _ := unix.Poll(fds, 40); n == 0 {
				return 27, nil
			}
		}
		first, _, err := t.keys.ReadRune()
		if err != nil {
			return 27, nil
		}
		if first == '[' || first == 'O' {
			if t.keys.Buffered() == 0 {
				fds := []unix.PollFd{{Fd: int32(t.tty.Fd()), Events: unix.POLLIN}}
				if n, _ := unix.Poll(fds, 40); n == 0 {
					return 0, nil
				}
			}
			second, _, err := t.keys.ReadRune()
			if err != nil {
				return 0, nil
			}
			switch second {
			case 'A':
				return 'k', nil
			case 'B':
				return 'j', nil
			}
		}
		return 27, nil
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
		fmt.Fprintf(t.tty, "\x1b[%d;1H\x1b[2K%s\x1b[?25h", h, fit(label+string(input), max(1, w-1)))
		r, err := t.key()
		if err != nil {
			return "", err
		}
		switch r {
		case 0:
			w, h = t.size()
			continue
		case 27:
			fmt.Fprint(t.tty, "\x1b[?25l")
			return "", errTUICancel
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
		fmt.Fprint(t.tty, fit(" "+title, max(1, w-1)), "\r\n")
		fmt.Fprint(t.tty, fit(" "+strings.Repeat("─", max(0, w-3)), max(1, w-1)), "\r\n")
		for i := 0; i < max(0, h-3); i++ {
			if offset+i < len(lines) {
				fmt.Fprint(t.tty, fit(lines[offset+i], max(1, w-1)))
			} else {
				fmt.Fprint(t.tty, "")
			}
			fmt.Fprint(t.tty, "\r\n")
		}
		fmt.Fprint(t.tty, fit(" ↑↓ scroll   Enter/Esc back", max(1, w-1)))
		key, err := t.key()
		for key == 0 && err == nil {
			if nextW, nextH := t.size(); nextW != w || nextH != h {
				break
			}
			key, err = t.key()
		}
		if err != nil {
			return err
		}
		switch key {
		case 'q', 27, '\r', '\n':
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
		w, h := t.size()
		var key rune
		var err error
		for {
			key, err = t.key()
			if key != 0 || err != nil {
				break
			}
			if nextW, nextH := t.size(); nextW != w || nextH != h {
				break
			}
		}
		if errors.Is(err, errTUIQuit) {
			return nil
		}
		if err != nil {
			return err
		}
		if key == 'q' {
			return nil
		}
		if key == 'j' {
			t.move(1)
			continue
		}
		if key == 'k' {
			t.move(-1)
			continue
		}
		if key == '\t' {
			t.showAll = !t.showAll
			t.ensureSelection()
			t.message = ""
			continue
		}
		if key == '/' {
			query, err := t.prompt(" Search: ")
			if errors.Is(err, errTUICancel) {
				continue
			}
			if err != nil {
				return err
			}
			t.query = strings.TrimSpace(query)
			t.ensureSelection()
			t.message = ""
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
			if err := t.view("Keyboard shortcuts", "BROWSE\n  ↑/↓ or j/k  Move     /  Search names and descriptions\n  Tab         Toggle focus / all services\n  Enter       Status   v  Unit file   l  Recent logs\n\nMANAGE\n  n  Create       e  Set a setting   u  Unset a setting\n  c  Clone        s  Start           t  Stop\n  r  Restart      b  Enable/disable  m  Mask/unmask\n  x  Remove local unit             R  Refresh\n\n  Esc cancels a prompt. q quits.\n\nSettings: exec, workdir, restart, env.NAME,\nor Section.Key such as Service.TimeoutStartSec."); err != nil && !errors.Is(err, errTUIQuit) {
				return err
			}
			continue
		}
		if err := t.action(key); errors.Is(err, errTUIQuit) {
			return nil
		} else if errors.Is(err, errTUICancel) {
			continue
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
		if err != nil && strings.TrimSpace(output) == "" {
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
