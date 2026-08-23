package tts

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// commandEngine runs a local text-to-speech executable — the on-prem
// option that needs no network at all: piper, espeak-ng, flite, mimic3,
// pico2wave, macOS say, festival's text2wave, or any wrapper script.
//
// The command line is split into argv by Northplane (quotes supported)
// and executed directly — never through a shell — with the placeholders
// {text} {lang} {voice} {rate} {out} substituted per argument. The text
// is additionally written to stdin, which is what piper/espeak-ng/say
// expect. Audio is read from the temp file named by {out} when that
// placeholder is used, otherwise from stdout.
//
// Examples:
//
//	piper --model /opt/piper/{voice}.onnx --output_file {out}
//	espeak-ng -v {lang} -s 160 --stdout
//	flite -voice slt -o {out}
//	say -v Anna -o {out} --data-format=LEI16@22050
//	/usr/local/bin/my-tts.sh {lang} {out}
//
// Security: the executable must be on the allowlist (config tts.commands;
// default = well-known TTS binaries by bare name, resolved via PATH; "*" =
// any) because profile editing only needs config:write; env entries that
// hook the dynamic loader or interpreters (LD_*, DYLD_*, PATH, …) are
// rejected for the same reason.
type commandEngine struct {
	argv    []string
	format  string
	outExt  string
	env     []string
	workDir string
	timeout time.Duration
}

// defaultCommandAllow are TTS binaries that take text and produce audio —
// the out-of-the-box allowlist when the server config names none.
var defaultCommandAllow = []string{
	"piper", "piper-tts", "espeak", "espeak-ng", "flite", "mimic3", "mimic", "pico2wave",
	"say", "text2wave", "festival", "tts", "coqui-tts", "balcon", "larynx", "sherpa-onnx-offline-tts",
	"np-tts", "northplane-tts",
}

func newCommandEngine(cfg map[string]string, get func(string) string, opts EngineOptions) (Engine, error) {
	argv, err := splitCommand(get("command"))
	if err != nil {
		return nil, fmt.Errorf("command: %w", err)
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("command: config.command required")
	}
	if err := checkCommandAllowed(argv[0], opts.AllowCommands); err != nil {
		return nil, err
	}
	e := &commandEngine{argv: argv, format: get("format"), outExt: strings.TrimPrefix(get("outExt"), "."),
		workDir: get("workDir"), timeout: opts.CommandTimeout}
	if e.outExt == "" {
		e.outExt = "wav"
	}
	if v := atof(get("timeoutSeconds"), 0); v > 0 {
		e.timeout = time.Duration(v * float64(time.Second))
	}
	if e.timeout <= 0 {
		e.timeout = 30 * time.Second
	}
	for _, kv := range strings.Split(get("env"), ";") {
		if kv = strings.TrimSpace(kv); kv != "" && strings.Contains(kv, "=") {
			if err := envAllowed(kv); err != nil {
				return nil, err
			}
			e.env = append(e.env, kv)
		}
	}
	return e, nil
}

// checkCommandAllowed enforces the allowlist: "*" permits anything; an
// entry with a directory component must match the configured path
// exactly; a bare entry ("piper") permits only the bare name, resolved
// through PATH — never /some/dir/piper, which would let a config writer
// run any file that happens to be named like a TTS binary.
func checkCommandAllowed(exe string, allow []string) error {
	if len(allow) == 0 {
		allow = defaultCommandAllow
	}
	for _, a := range allow {
		if a == "*" || a == exe {
			return nil
		}
	}
	return fmt.Errorf("command: executable %q is not allowed (server config tts.commands; default: %s)",
		exe, strings.Join(defaultCommandAllow, ", "))
}

// blockedEnv lists environment variables a profile may not set for the
// child process: loader/interpreter hooks would turn an allowlisted TTS
// binary into arbitrary code execution.
var blockedEnv = []string{"PATH", "PYTHONPATH", "PYTHONSTARTUP", "PYTHONHOME", "NODE_OPTIONS", "PERL5OPT",
	"PERL5LIB", "RUBYOPT", "RUBYLIB", "GCONV_PATH", "IFS", "BASH_ENV", "ENV", "SHELLOPTS", "PS4", "NLSPATH",
	"LOCALDOMAIN", "RES_OPTIONS", "HOSTALIASES", "TMPDIR", "MALLOC_CHECK_", "GLIBC_TUNABLES"}

func envAllowed(kv string) error {
	key, _, _ := strings.Cut(kv, "=")
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("command: env entry %q has no name", kv)
	}
	for _, r := range key {
		ok := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_'
		if !ok {
			return fmt.Errorf("command: env name %q invalid", key)
		}
	}
	up := strings.ToUpper(key)
	if strings.HasPrefix(up, "LD_") || strings.HasPrefix(up, "DYLD_") {
		return fmt.Errorf("command: env %s is not allowed", key)
	}
	for _, b := range blockedEnv {
		if up == b {
			return fmt.Errorf("command: env %s is not allowed", key)
		}
	}
	return nil
}

// splitCommand splits a command line into argv, honouring single and
// double quotes and backslash escapes (no globbing, no expansion).
func splitCommand(s string) ([]string, error) {
	var argv []string
	var cur strings.Builder
	inArg := false
	quote := rune(0)
	escape := false
	for _, r := range s {
		switch {
		case escape:
			cur.WriteRune(r)
			escape = false
		case r == '\\' && quote != '\'':
			escape = true
			inArg = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			inArg = true
		case r == ' ' || r == '\t' || r == '\n':
			if inArg {
				argv = append(argv, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unbalanced quote")
	}
	if escape {
		return nil, fmt.Errorf("trailing backslash")
	}
	if inArg {
		argv = append(argv, cur.String())
	}
	return argv, nil
}

func (e *commandEngine) Synthesize(ctx context.Context, req Request) (*Audio, error) {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	outPath := ""
	useOut := false
	for _, a := range e.argv {
		if strings.Contains(a, "{out}") {
			useOut = true
			break
		}
	}
	if useOut {
		f, err := os.CreateTemp("", "np-tts-*."+e.outExt)
		if err != nil {
			return nil, fmt.Errorf("command: temp file: %w", err)
		}
		outPath = f.Name()
		_ = f.Close()
		defer func() { _ = os.Remove(outPath) }()
	}
	rate := "1"
	if req.Rate > 0 {
		rate = fmt.Sprintf("%g", req.Rate)
	}
	repl := strings.NewReplacer("{text}", req.Text, "{lang}", req.Lang, "{voice}", req.Voice,
		"{rate}", rate, "{out}", outPath)
	argv := make([]string, len(e.argv))
	for i, a := range e.argv {
		argv[i] = repl.Replace(a)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(req.Text + "\n")
	cmd.Dir = e.workDir
	if len(e.env) > 0 {
		cmd.Env = append(os.Environ(), e.env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("command %s: %w: %s", filepath.Base(argv[0]), err, firstLine(stderr.String()))
	}
	var data []byte
	if useOut {
		b, err := os.ReadFile(outPath)
		if err != nil {
			return nil, fmt.Errorf("command: read output: %w", err)
		}
		data = b
	} else {
		data = stdout.Bytes()
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("command %s produced no audio: %s", filepath.Base(argv[0]), firstLine(stderr.String()))
	}
	return Decode(data, "", e.format)
}
