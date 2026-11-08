// Package fake is a sandbox provider that keeps nothing on the host, which
// proves no caller depends on a host path. It records every call and answers
// Exec from a function.
//
// Download returns the sandbox's last upload, whatever the path, so a caller
// that moves an archive from one sandbox to another sees it arrive.
package fake

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/ivy/almanac/sandbox"
)

// Sandbox is a fake provider. Its methods are safe for concurrent use.
type Sandbox struct {
	reply func(command string) sandbox.Output

	mu       sync.Mutex
	next     int
	alive    map[sandbox.ID][][]byte
	commands []Command
	specs    []sandbox.Spec
}

// Command is one Exec call.
type Command struct {
	Sandbox sandbox.ID
	Line    string
}

var _ sandbox.Sandbox = (*Sandbox)(nil)

// New is a fake whose every command succeeds and prints nothing.
func New() *Sandbox {
	return Answering(func(string) sandbox.Output { return sandbox.Output{} })
}

// Answering is a fake that answers each command with reply.
func Answering(reply func(command string) sandbox.Output) *Sandbox {
	return &Sandbox{reply: reply, alive: map[sandbox.ID][][]byte{}}
}

// Commands lists every command run, in order.
func (f *Sandbox) Commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	lines := make([]string, len(f.commands))
	for i, c := range f.commands {
		lines[i] = c.Line
	}
	return lines
}

// Calls lists every command run, with the sandbox each ran in.
func (f *Sandbox) Calls() []Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.commands)
}

// Specs lists the spec of every sandbox created, in order.
func (f *Sandbox) Specs() []sandbox.Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.specs)
}

// Alive counts the sandboxes created and not yet destroyed.
func (f *Sandbox) Alive() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.alive)
}

// Create refuses what every provider refuses, and otherwise always succeeds.
func (f *Sandbox) Create(ctx context.Context, spec sandbox.Spec) (sandbox.ID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := spec.Validate(); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	id := sandbox.ID(fmt.Sprintf("fake-%d", f.next))
	f.alive[id] = nil
	f.specs = append(f.specs, spec)
	return id, nil
}

// Exec records command and answers it. It never runs anything.
func (f *Sandbox) Exec(ctx context.Context, id sandbox.ID, command string, _ time.Duration) (sandbox.Output, error) {
	if err := ctx.Err(); err != nil {
		return sandbox.Output{}, err
	}
	f.mu.Lock()
	if _, ok := f.alive[id]; !ok {
		f.mu.Unlock()
		return sandbox.Output{}, notFound("exec", id)
	}
	f.commands = append(f.commands, Command{Sandbox: id, Line: command})
	f.mu.Unlock()
	return f.reply(command), nil
}

// Upload keeps archive, for Download to return.
func (f *Sandbox) Upload(ctx context.Context, id sandbox.ID, archive []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	uploads, ok := f.alive[id]
	if !ok {
		return notFound("upload", id)
	}
	f.alive[id] = append(uploads, slices.Clone(archive))
	return nil
}

// Download returns the last archive uploaded to id, or nothing.
func (f *Sandbox) Download(ctx context.Context, id sandbox.ID, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := sandbox.CheckPath(path); err != nil {
		return nil, &sandbox.Error{Op: "download", Sandbox: id, Err: err}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	uploads, ok := f.alive[id]
	if !ok {
		return nil, notFound("download", id)
	}
	if len(uploads) == 0 {
		return []byte{}, nil
	}
	return slices.Clone(uploads[len(uploads)-1]), nil
}

// Destroy forgets id.
func (f *Sandbox) Destroy(_ context.Context, id sandbox.ID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.alive, id)
	return nil
}

func notFound(op string, id sandbox.ID) error {
	return &sandbox.Error{Op: op, Sandbox: id, Err: sandbox.ErrNotFound}
}
