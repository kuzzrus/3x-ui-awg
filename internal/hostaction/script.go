package hostaction

import (
	"os"
	"strings"
)

var proxyEnvKeys = []string{"https_proxy", "HTTPS_PROXY", "all_proxy", "ALL_PROXY", "http_proxy", "HTTP_PROXY", "no_proxy", "NO_PROXY"}

// script is the whole of what a unit runs: systemd would rewrite $ and ${} in a command line, so the command is quoted
// into a file instead, and the file records how the command ended.
func (r *run) script() string {
	var b strings.Builder
	b.WriteString("#!/bin/bash\n")
	b.WriteString("dir=" + shellQuote(r.dir) + "\n")
	b.WriteString("export HOSTACTION_DIR=\"$dir\"\n")
	if r.spec.Stdin != "" {
		b.WriteString("exec 0<\"$dir/stdin\"\n")
	} else {
		b.WriteString("exec 0</dev/null\n")
	}
	b.WriteString(quoteAll(r.command()) + " >\"$dir/stdout\" 2>\"$dir/stderr\"\n")
	b.WriteString("code=$?\n")
	b.WriteString("printf '%s\\n' \"$code\" >\"$dir/status.tmp\" && mv -f \"$dir/status.tmp\" \"$dir/status\"\n")
	b.WriteString("exit \"$code\"\n")
	return b.String()
}

// command is the command line, behind env when there are settings to hand over.
func (r *run) command() []string {
	env := r.env()
	if len(env) == 0 {
		return r.spec.Argv
	}
	return append(append([]string{"env"}, env...), r.spec.Argv...)
}

// ProxyEnv is the proxy settings the panel was started with as NAME=value, which a transient unit would not otherwise see.
func ProxyEnv() []string {
	var env []string
	for _, key := range proxyEnvKeys {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}

// env is the caller's settings plus the panel's proxy settings.
func (r *run) env() []string {
	env := append([]string(nil), r.spec.Env...)
	have := make(map[string]bool, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		have[name] = true
	}
	for _, entry := range ProxyEnv() {
		if name, _, _ := strings.Cut(entry, "="); !have[name] {
			env = append(env, entry)
		}
	}
	return env
}

func quoteAll(words []string) string {
	quoted := make([]string, len(words))
	for i, word := range words {
		quoted[i] = shellQuote(word)
	}
	return strings.Join(quoted, " ")
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// label turns a name into the part of a unit name that says what runs.
func label(name string) string {
	var b strings.Builder
	dash := true
	for _, c := range strings.ToLower(name) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 24 {
		out = strings.Trim(out[:24], "-")
	}
	if out == "" {
		return "action"
	}
	return out
}
