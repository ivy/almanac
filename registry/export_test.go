package registry

// WithMaxBytes lowers what r fetches, so a spec can pass the limit without sending 256 MiB.
func WithMaxBytes(r *HTTP, limit int64) *HTTP {
	r.maxBytes = limit
	return r
}
