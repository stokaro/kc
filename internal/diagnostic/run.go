// Package diagnostic runs a local diagnostic tool once and turns its
// output into a report kc can show. It starts the tool without a shell,
// bounds its time and output, and tells a missing tool, a timeout, a
// cancellation, a failed process and unreadable output apart.
package diagnostic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Outcome is how a run ended.
type Outcome int

const (
	// Finished means the tool exited with status 0.
	Finished Outcome = iota
	// Unavailable means the executable is not in PATH.
	Unavailable
	// TimedOut means the run hit its timeout and was stopped.
	TimedOut
	// Canceled means the caller stopped the run.
	Canceled
	// Failed means the tool did not start, exited with a non-zero status
	// or wrote more output than the limit.
	Failed
)

func (o Outcome) String() string {
	switch o {
	case Finished:
		return "finished"
	case Unavailable:
		return "unavailable"
	case TimedOut:
		return "timed out"
	case Canceled:
		return "canceled"
	case Failed:
		return "failed"
	}
	return fmt.Sprintf("outcome(%d)", int(o))
}

// DefaultMaxOutput bounds the standard output a run keeps.
const DefaultMaxOutput = 32 << 20

// stderrTail is how much of the standard error a run keeps, from the end.
const stderrTail = 16 << 10

// stopGrace is how long a stopped tool gets after the interrupt before it
// is killed.
const stopGrace = 3 * time.Second

// Process is one run of a tool.
type Process struct {
	// Argv is the executable and its arguments, passed without a shell.
	Argv []string
	// Env is the full environment; nil inherits kc's.
	Env []string
	// Timeout stops the run; zero means none.
	Timeout time.Duration
	// MaxOutput bounds the standard output; zero means DefaultMaxOutput.
	MaxOutput int
}

// Result is what a run produced.
type Result struct {
	Outcome Outcome
	// Stdout is the standard output, complete only for Finished.
	Stdout []byte
	// Stderr is the end of the standard error.
	Stderr string
	// ExitCode is the exit status, or -1 when the tool did not exit on its
	// own.
	ExitCode int
	// Err explains Unavailable and Failed.
	Err     error
	Elapsed time.Duration
}

// LookPath reports where the executable of argv is, or why it is
// unavailable.
func LookPath(argv []string) (string, error) {
	if len(argv) == 0 || argv[0] == "" {
		return "", errors.New("no executable")
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return "", fmt.Errorf("%s is not installed or not in PATH; kc does not install tools", argv[0])
	}
	return path, nil
}

// Run runs p until it exits, its timeout passes or ctx is done.
func Run(ctx context.Context, p Process) Result {
	start := time.Now()
	res := Result{ExitCode: -1}
	path, err := LookPath(p.Argv)
	if err != nil {
		res.Outcome, res.Err = Unavailable, err
		return res
	}
	runCtx := ctx
	if p.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, p.Timeout)
		defer cancel()
	}
	limit := p.MaxOutput
	if limit <= 0 {
		limit = DefaultMaxOutput
	}
	stdout := &limitedBuffer{limit: limit}
	stderr := &tailBuffer{limit: stderrTail}
	// #nosec G204 -- the executable and arguments come from the user's
	// own configuration and are passed without a shell.
	cmd := exec.CommandContext(runCtx, path, p.Argv[1:]...)
	cmd.Env = p.Env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Cancel = func() error {
		// Ask first, so the tool can clean up; WaitDelay kills it after.
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = stopGrace
	err = cmd.Run()
	res.Elapsed = time.Since(start)
	res.Stderr = stderr.String()
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	switch {
	case ctx.Err() != nil:
		res.Outcome = Canceled
	case runCtx.Err() != nil:
		res.Outcome = TimedOut
	case stdout.overflow:
		res.Outcome = Failed
		res.Err = fmt.Errorf("output is larger than %s; kc discarded it", bytesText(float64(limit)))
	case err != nil:
		res.Outcome = Failed
		if _, ok := errors.AsType[*exec.ExitError](err); ok {
			res.Err = fmt.Errorf("exit status %d", res.ExitCode)
		} else {
			res.Err = err
		}
	default:
		res.Outcome = Finished
		res.Stdout = stdout.Bytes()
	}
	return res
}

// limitedBuffer keeps the first limit bytes and drops the rest, so a
// runaway tool cannot fill kc's memory. It keeps accepting writes, so
// the tool does not die on a broken pipe. The buffer is a field, not
// embedded: an embedded bytes.Buffer would lend its ReadFrom to io.Copy,
// which then bypasses Write and the limit.
type limitedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room < len(p) {
		b.overflow = true
		if room > 0 {
			b.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) Bytes() []byte { return b.buf.Bytes() }

// tailBuffer keeps the last limit bytes.
type tailBuffer struct {
	buf   []byte
	limit int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.limit; over > 0 {
		b.buf = append(b.buf[:0], b.buf[over:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string { return string(b.buf) }
