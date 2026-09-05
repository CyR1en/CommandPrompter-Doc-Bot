package sourcegit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const maximumGitDiagnosticBytes = 262_144

var errCommandOutputLimit = errors.New("git command output limit exceeded")
var errCommandDenied = errors.New("git command is outside the allowlist")

// requireCurloptResolve verifies the DNS-pinning primitive before any remote
// URL is accepted. Unknown Git configuration keys are otherwise silently
// ignored, which would turn a seemingly pinned request back into ambient DNS.
func requireCurloptResolve(ctx context.Context) error {
	command := exec.CommandContext(ctx, "git", "help", "--config")
	command.Env = append(baseGitEnvironment(), "GIT_PAGER=cat")
	command.Stdin = nil
	stdout := &boundedBuffer{limit: 64 * 1024}
	stderr := &boundedBuffer{limit: 4 * 1024}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil || stdout.exceeded || stderr.exceeded || ctx.Err() != nil {
		return errors.New("Git capability probe failed")
	}
	for _, name := range strings.Fields(string(stdout.buffer.Bytes())) {
		if name == "http.curloptResolve" {
			return nil
		}
	}
	return errors.New("Git capability is unavailable")
}

type commandResult struct {
	stdout   []byte
	stderr   []byte
	exitCode int
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	if buffer.exceeded {
		return len(value), errCommandOutputLimit
	}
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining < len(value) {
		if remaining > 0 {
			_, _ = buffer.buffer.Write(value[:remaining])
		}
		buffer.exceeded = true
		return len(value), errCommandOutputLimit
	}
	return buffer.buffer.Write(value)
}

func executeGit(
	ctx context.Context,
	arguments []string,
	environment []string,
	cwd string,
	stdoutLimit int,
	timeout time.Duration,
	input []byte,
) (commandResult, error) {
	if stdoutLimit <= 0 || timeout <= 0 {
		return commandResult{}, errors.New("invalid Git execution bound")
	}
	if !allowedGitCommand(arguments) {
		return commandResult{}, errCommandDenied
	}
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(commandContext, "git", arguments...)
	command.Dir = cwd
	command.Env = append([]string(nil), environment...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.WaitDelay = time.Second
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	} else {
		command.Stdin = nil
	}
	stdout := &boundedBuffer{limit: stdoutLimit}
	stderr := &boundedBuffer{limit: maximumGitDiagnosticBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	result := commandResult{stdout: stdout.buffer.Bytes(), stderr: stderr.buffer.Bytes()}
	if stdout.exceeded || stderr.exceeded || errors.Is(err, errCommandOutputLimit) {
		return result, errCommandOutputLimit
	}
	if commandContext.Err() != nil {
		return result, commandContext.Err()
	}
	if err == nil {
		return result, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.exitCode = exitError.ExitCode()
		return result, nil
	}
	return result, err
}

func allowedGitCommand(arguments []string) bool {
	index := 0
	for index < len(arguments) {
		switch arguments[index] {
		case "-c":
			if index+1 >= len(arguments) || !allowedGitConfiguration(arguments[index+1]) {
				return false
			}
			index += 2
		case "--git-dir":
			if index+1 >= len(arguments) || arguments[index+1] == "" || strings.HasPrefix(arguments[index+1], "-") {
				return false
			}
			index += 2
		default:
			_, allowed := map[string]struct{}{
				"init": {}, "config": {}, "rev-parse": {}, "fetch": {},
				"ls-tree": {}, "cat-file": {}, "ls-remote": {},
			}[arguments[index]]
			return allowed
		}
	}
	return false
}

func allowedGitConfiguration(value string) bool {
	if strings.HasPrefix(value, "http.curloptResolve=") {
		return len(value) > len("http.curloptResolve=")
	}
	_, allowed := map[string]struct{}{
		"protocol.allow=never":        {},
		"protocol.https.allow=always": {},
		"protocol.file.allow=never":   {},
		"protocol.file.allow=always":  {},
		"protocol.ext.allow=never":    {},
		"http.followRedirects=false":  {},
		"http.maxRequests=1":          {},
		"http.sslVerify=true":         {},
		"credential.helper=":          {},
		"core.hooksPath=/dev/null":    {},
		"submodule.recurse=false":     {},
	}[value]
	return allowed
}

func baseGitEnvironment() []string {
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/bin:/bin"
	}
	return []string{
		"PATH=" + path,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	}
}

func cloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	cloned := make([]byte, len(value))
	copy(cloned, value)
	return cloned
}

var _ io.Writer = (*boundedBuffer)(nil)
