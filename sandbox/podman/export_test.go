package podman

// Exposed to the specs in podman_test.
var (
	CreateArgs  = createArgs
	NewID       = newID
	OutputLimit = outputLimit
)

// Tail is the writer Exec keeps a stream's end in.
type Tail = tail

// NewTail keeps the last limit bytes.
func NewTail(limit int) *Tail { return &tail{limit: limit} }

// Truncated reports whether t dropped anything.
func (t *tail) Truncated() bool { return t.truncated() }
