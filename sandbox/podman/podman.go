// Package podman is a sandbox provider on rootless Podman. Files move with
// `podman cp`, never a bind mount, so it stays interchangeable with a remote
// provider. The flags are the ones ADR 1 names; see
// docs/adrs/0001-agent-sandbox.md.
package podman

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ivy/almanac/sandbox"
)

const (
	// unprivileged is the user every sandboxed process runs as.
	unprivileged = "1000:1000"
	// scratchMB is the size of the private /tmp.
	scratchMB = 1024
	// execGrace is how long past its limit a command gets before the client
	// gives up on it too.
	execGrace = 5 * time.Second
	// killed is the exit code of a command `timeout -s KILL` killed.
	killed = 137
	// podmanFailed is the exit code of `podman exec` when podman itself,
	// not the command, failed.
	podmanFailed = 125
	// outputLimit is how much of each stream Exec keeps. A command can
	// print without end, and the host must not hold all of it.
	outputLimit = 8 << 20
	// setupTimeout bounds the commands Create runs in a new sandbox.
	setupTimeout = 30 * time.Second
	// waitDelay is how long a podman client may hold its pipes open after
	// it is killed.
	waitDelay = 2 * time.Second
)

// scratch is HOME and TMPDIR. Tools write state there, and the checkout must
// hold only the patch.
var scratch = sandbox.Workdir + "/.tmp"

// Sandbox runs sandboxes as rootless Podman containers. The zero value is
// ready to use.
type Sandbox struct{}

var _ sandbox.Sandbox = (*Sandbox)(nil)

// New returns a Podman provider.
func New() *Sandbox { return &Sandbox{} }

// Create starts a container that drops every capability, has a read-only root,
// writes only to an in-memory Workdir and /tmp, and has no network unless spec
// is a fetch sandbox with egress. Its main process sleeps for spec.Lifetime and
// the container is removed when it exits, so one nobody destroys ends itself.
func (p *Sandbox) Create(ctx context.Context, spec sandbox.Spec) (sandbox.ID, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	id, err := newID()
	if err != nil {
		return "", &sandbox.Error{Op: "create", Err: err}
	}
	_, err = podman(ctx, "create", id, nil, createArgs(id, spec)...)
	if err == nil {
		mkdir := "mkdir -p " + scratch
		var out sandbox.Output
		out, err = p.Exec(ctx, id, mkdir, setupTimeout)
		if err == nil && out.ExitCode != 0 {
			err = &sandbox.Error{Op: "create", Sandbox: id, Err: &sandbox.CommandFailed{Command: mkdir, Output: out}}
		}
	}
	if err != nil {
		// The container may have started even if podman reported a failure.
		return "", errors.Join(err, p.Destroy(context.WithoutCancel(ctx), id))
	}
	return id, nil
}

// newID names a sandbox almanac-PID-RANDOM, after the process that made it. A process killed
// outright cannot end its sandboxes, so whatever supervised it stops them by that prefix.
func newID() (sandbox.ID, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return sandbox.ID(fmt.Sprintf("almanac-%d-%s", os.Getpid(), hex.EncodeToString(b))), nil
}

// createArgs is the `podman` command line that starts a sandbox named id.
func createArgs(id sandbox.ID, spec sandbox.Spec) []string {
	args := []string{
		"run", "--detach", "--rm",
		"--name", string(id),
		"--cap-drop=all",
		"--security-opt=no-new-privileges",
		"--read-only",
		// Podman would otherwise mount writable tmpfs on /tmp, /var/tmp and /run.
		"--read-only-tmpfs=false",
		"--user", unprivileged,
		"--workdir", sandbox.Workdir,
		fmt.Sprintf("--memory=%dm", spec.MemoryMB),
		fmt.Sprintf("--cpus=%d", spec.CPUs),
		fmt.Sprintf("--pids-limit=%d", spec.PIDs),
		"--tmpfs", fmt.Sprintf("%s:rw,exec,size=%dm,mode=0755,U", sandbox.Workdir, spec.WorkMB),
		// Some tools insist on /tmp whatever TMPDIR says. This one is private
		// and in memory, and it goes when the sandbox does.
		"--tmpfs", fmt.Sprintf("/tmp:rw,size=%dm,mode=1777", scratchMB),
	}
	if spec.Egress.None() {
		args = append(args, "--network=none")
	}
	for _, name := range slices.Sorted(maps.Keys(spec.Env)) {
		args = append(args, "--env", name+"="+spec.Env[name])
	}
	// Last, so they win over spec.Env.
	args = append(args, "--env", "TMPDIR="+scratch, "--env", "HOME="+scratch)
	lifetime := max(int(spec.Lifetime/time.Second), 1)
	return append(args, spec.Image, "sleep", strconv.Itoa(lifetime))
}

// Exec runs command under `timeout -s KILL` inside the sandbox, so the limit
// holds even if this client is killed.
func (p *Sandbox) Exec(ctx context.Context, id sandbox.ID, command string, timeout time.Duration) (sandbox.Output, error) {
	seconds := max(int(timeout/time.Second), 1)
	limited, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second+execGrace)
	defer cancel()

	stdout, stderr := &tail{limit: outputLimit}, &tail{limit: outputLimit}
	cmd := exec.CommandContext(limited, "podman", "exec", string(id),
		"timeout", "-s", "KILL", strconv.Itoa(seconds), "sh", "-c", command)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = waitDelay
	err := cmd.Run()

	if ctx.Err() != nil {
		return sandbox.Output{}, &sandbox.Error{Op: "exec", Sandbox: id, Err: ctx.Err()}
	}
	if limited.Err() != nil {
		return sandbox.Output{ExitCode: killed, Stderr: "killed: over the time limit", TimedOut: true}, nil
	}
	code := 0
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
		if code < 0 {
			code = killed
		}
	default:
		return sandbox.Output{}, &sandbox.Error{Op: "exec", Sandbox: id, Err: cannotRun(err)}
	}
	// 125 is podman's own failure, but a command can exit with it too.
	if code == podmanFailed && !p.exists(ctx, id) {
		return sandbox.Output{}, &sandbox.Error{Op: "exec", Sandbox: id, Err: sandbox.ErrNotFound}
	}
	return sandbox.Output{
		ExitCode:  code,
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		TimedOut:  code == killed,
		Truncated: stdout.truncated() || stderr.truncated(),
	}, nil
}

// Upload streams archive into `podman cp`, which unpacks it in Workdir.
func (p *Sandbox) Upload(ctx context.Context, id sandbox.ID, archive []byte) error {
	_, err := podman(ctx, "upload", id, archive, "cp", "-", string(id)+":"+sandbox.Workdir)
	return p.orNotFound(ctx, "upload", id, err)
}

// Download reads path out with `podman cp`, which writes a tar archive.
func (p *Sandbox) Download(ctx context.Context, id sandbox.ID, path string) ([]byte, error) {
	clean, err := sandbox.CheckPath(path)
	if err != nil {
		return nil, &sandbox.Error{Op: "download", Sandbox: id, Err: err}
	}
	out, err := podman(ctx, "download", id, nil, "cp", string(id)+":"+sandbox.Workdir+"/"+clean, "-")
	return out, p.orNotFound(ctx, "download", id, err)
}

// Destroy removes the container at once, without waiting for its process.
func (p *Sandbox) Destroy(ctx context.Context, id sandbox.ID) error {
	_, err := podman(ctx, "destroy", id, nil, "rm", "--force", "--ignore", "--time", "0", string(id))
	return err
}

// exists reports whether the container is there. If podman cannot answer, it
// says yes, so the caller reports podman's own error rather than a vanished
// sandbox.
func (p *Sandbox) exists(ctx context.Context, id sandbox.ID) bool {
	err := exec.CommandContext(ctx, "podman", "container", "exists", string(id)).Run()
	var exitErr *exec.ExitError
	return !errors.As(err, &exitErr) || exitErr.ExitCode() != 1
}

// orNotFound turns a failure on a container that is gone into ErrNotFound.
func (p *Sandbox) orNotFound(ctx context.Context, op string, id sandbox.ID, err error) error {
	if err != nil && !p.exists(ctx, id) {
		return &sandbox.Error{Op: op, Sandbox: id, Err: sandbox.ErrNotFound}
	}
	return err
}

// podman runs podman with args and stdin, and returns what it printed. A
// non-zero exit is an error carrying podman's own message.
func podman(ctx context.Context, op string, id sandbox.ID, stdin []byte, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "podman", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = waitDelay
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return stdout.Bytes(), nil
	case ctx.Err() != nil:
		return nil, &sandbox.Error{Op: op, Sandbox: id, Err: ctx.Err()}
	case errors.As(err, &exitErr):
		reason := strings.TrimSpace(stderr.String())
		if reason == "" {
			reason = exitErr.Error()
		}
		return nil, &sandbox.Error{Op: op, Sandbox: id, Err: fmt.Errorf("podman: %s", reason)}
	default:
		return nil, &sandbox.Error{Op: op, Sandbox: id, Err: cannotRun(err)}
	}
}

func cannotRun(err error) error {
	return fmt.Errorf("cannot run podman: %w; install rootless Podman and check that `podman info` works for this user", err)
}

// tail keeps the last limit bytes written to it. It holds up to twice that
// between compactions, so a long stream of small writes is not copied each time.
type tail struct {
	limit   int
	buf     []byte
	dropped bool
}

func (t *tail) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > t.limit {
		p = p[len(p)-t.limit:]
		t.dropped = true
	}
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2*t.limit {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.limit:]...)
		t.dropped = true
	}
	return n, nil
}

func (t *tail) truncated() bool { return t.dropped || len(t.buf) > t.limit }

// String is the text kept, with any bytes that are not UTF-8 replaced, as
// cutting at a limit can split a character.
func (t *tail) String() string {
	kept := t.buf[max(len(t.buf)-t.limit, 0):]
	return strings.ToValidUTF8(string(kept), "\uFFFD")
}
