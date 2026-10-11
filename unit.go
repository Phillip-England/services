package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]*$`)
var keyPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)
var envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func serviceName(s string) (string, error) {
	s = strings.TrimSuffix(s, ".service")
	if !namePattern.MatchString(s) || s == "." || s == ".." {
		return "", fmt.Errorf("invalid service name %q", s)
	}
	return s + ".service", nil
}

type unitFile map[string]map[string]string

func (u unitFile) set(section, key, value string) {
	if u[section] == nil {
		u[section] = map[string]string{}
	}
	u[section][key] = value
}

func (u unitFile) remove(section, key string) {
	delete(u[section], key)
	if len(u[section]) == 0 {
		delete(u, section)
	}
}

func parseUnit(data []byte) (unitFile, error) {
	u := unitFile{}
	section := ""
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			if !keyPattern.MatchString(section) {
				return nil, fmt.Errorf("invalid section on line %d", i+1)
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section == "" || !keyPattern.MatchString(key) {
			return nil, fmt.Errorf("invalid directive on line %d", i+1)
		}
		u.set(section, key, value)
	}
	return u, nil
}

func (u unitFile) render() []byte {
	var b strings.Builder
	sections := make([]string, 0, len(u))
	for section := range u {
		sections = append(sections, section)
	}
	sort.Strings(sections)
	for _, section := range sections {
		fmt.Fprintf(&b, "[%s]\n", section)
		keys := make([]string, 0, len(u[section]))
		for key := range u[section] {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if section == "Service" && key == "ExecStart" {
				b.WriteString("ExecStart=\n")
			}
			fmt.Fprintf(&b, "%s=%s\n", key, u[section][key])
		}
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func directive(input string) (string, string, error) {
	aliases := map[string]string{
		"exec": "Service.ExecStart", "workdir": "Service.WorkingDirectory",
		"user": "Service.User", "group": "Service.Group", "restart": "Service.Restart",
		"restart-sec": "Service.RestartSec", "type": "Service.Type",
		"description": "Unit.Description", "after": "Unit.After",
		"wants": "Unit.Wants", "wanted-by": "Install.WantedBy",
	}
	if target, ok := aliases[strings.ToLower(input)]; ok {
		input = target
	}
	section, key, ok := strings.Cut(input, ".")
	if !ok || !keyPattern.MatchString(section) || !keyPattern.MatchString(key) {
		return "", "", fmt.Errorf("invalid key %q; use an alias or Section.Key", input)
	}
	return section, key, nil
}

func validateValue(v string) error {
	if strings.ContainsAny(v, "\r\n\x00") {
		return fmt.Errorf("values cannot contain newlines or NUL bytes")
	}
	return nil
}

func quoteEnvironment(key, value string) (string, error) {
	if !envPattern.MatchString(key) {
		return "", fmt.Errorf("invalid environment variable %q", key)
	}
	if err := validateValue(value); err != nil {
		return "", err
	}
	// systemd accepts double quoted KEY=VALUE assignments. Escape characters
	// interpreted by systemd's unit parser, including its percent specifier.
	v := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "%", "%%").Replace(key + "=" + value)
	return "\"" + v + "\"", nil
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".services-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0644); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func createUnit(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".services-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0644); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Link(f.Name(), path); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%s already exists", path)
		}
		return err
	}
	return nil
}
