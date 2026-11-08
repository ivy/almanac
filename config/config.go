// Package config reads an almanac's runtime settings: almanac.toml, then
// ALMANAC_* variables, which win. Behaviour lives in the binary. Config can
// tighten limits but never widen them, and it has no way to name a capability,
// an effect, or a secret.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/ivy/almanac/diag"
)

// Config is what the operator set. The zero Config sets nothing.
type Config struct {
	// Listen is where the daemon answers health checks. Empty leaves it to
	// the binary.
	Listen string
	// The limits below are documented on [Limits]. Each tightens the binary's
	// own, and one left unset tightens nothing.
	MaxChainDepth      Cap
	RunsPerHour        Cap
	OverallRunsPerHour Cap
	Concurrency        Cap
	ScanInterval       time.Duration
}

// Tighten returns the limits a binary declared, tightened by this config.
func (c Config) Tighten(declared Limits) Limits {
	return declared.Tighten(Limits{
		MaxChainDepth:      c.MaxChainDepth.Or(declared.MaxChainDepth),
		RunsPerHour:        c.RunsPerHour,
		OverallRunsPerHour: c.OverallRunsPerHour,
		Concurrency:        c.Concurrency.Or(declared.Concurrency),
		ScanInterval:       c.ScanInterval,
	})
}

// number is a limit set by a whole number, in the file or the environment.
type number struct {
	key, env string
	max      int64
	set      func(*Config, int64)
}

// numbers are every limit config can set, in the order the docs list them.
var numbers = []number{
	{"max_chain_depth", "ALMANAC_MAX_CHAIN_DEPTH", math.MaxInt32,
		func(c *Config, n int64) { c.MaxChainDepth = CapAt(int(n)) }},
	{"runs_per_hour_cap", "ALMANAC_RUNS_PER_HOUR_CAP", math.MaxInt32,
		func(c *Config, n int64) { c.RunsPerHour = CapAt(int(n)) }},
	{"overall_runs_per_hour_cap", "ALMANAC_OVERALL_RUNS_PER_HOUR_CAP", math.MaxInt32,
		func(c *Config, n int64) { c.OverallRunsPerHour = CapAt(int(n)) }},
	{"concurrency", "ALMANAC_CONCURRENCY", math.MaxInt32,
		func(c *Config, n int64) { c.Concurrency = CapAt(int(n)) }},
	{"scan_interval_minutes", "ALMANAC_SCAN_INTERVAL_MINUTES", int64(math.MaxInt64 / time.Minute),
		func(c *Config, n int64) { c.ScanInterval = time.Duration(n) * time.Minute }},
}

const listenKey, listenEnv = "listen", "ALMANAC_LISTEN"

// Load reads the file ALMANAC_CONFIG names, or almanac.toml, then the
// ALMANAC_* variables. A missing file is no config, and runs on the binary's
// limits. A file that exists and cannot be read is refused, because running
// without it could run wider than the operator meant.
func Load() (Config, error) {
	path, ok := os.LookupEnv("ALMANAC_CONFIG")
	if !ok {
		path = "almanac.toml"
	}
	var config Config
	text, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return Config{}, diag.ReadingConfig(path, err)
	default:
		if config, err = Parse(path, string(text)); err != nil {
			return Config{}, err
		}
	}
	if err := config.readEnvironment(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (c *Config) readEnvironment() error {
	if value, ok := os.LookupEnv(listenEnv); ok {
		c.Listen = value
	}
	for _, n := range numbers {
		value, ok := os.LookupEnv(n.env)
		if !ok {
			continue
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 0 || parsed > n.max {
			return diag.InvalidEnv(n.env)
		}
		n.set(c, parsed)
	}
	return nil
}

// Parse reads a config file's text. Name is the file as the operator knows it.
// An error points at the line that caused it, and never shows a secret.
func Parse(name, text string) (Config, error) {
	file := lines(text)
	for _, l := range file {
		if l.secretKey != "" {
			source := diag.Source{Name: name, Text: redact(file)}
			return Config{}, diag.SecretInConfig(l.secretKey, source, l.mark(l.secretAt))
		}
	}

	source := diag.Source{Name: name, Text: text}
	var raw map[string]any
	meta, err := toml.Decode(text, &raw)
	if err != nil {
		var parse toml.ParseError
		if errors.As(err, &parse) {
			span := diag.Span{Offset: parse.Position.Start, Length: parse.Position.Len}
			return Config{}, diag.InvalidConfig(parse.Message, source, span)
		}
		return Config{}, diag.InvalidConfig(err.Error(), source, diag.Span{})
	}

	var config Config
	seen := map[string]bool{}
	for _, key := range meta.Keys() {
		top := key[0]
		if seen[top] {
			continue
		}
		seen[top] = true
		if err := config.set(top, raw[top]); err != nil {
			l := find(file, top)
			if errors.Is(err, errUnknown) {
				return Config{}, diag.UnknownConfigKey(key.String(), source, l.mark(l.key))
			}
			// Point at the value, unless the key holds a table instead.
			at := l.value
			if l.header || len(l.path) != 1 {
				at = l.key
			}
			return Config{}, diag.InvalidConfig(err.Error(), source, l.mark(at))
		}
	}
	return config, nil
}

var errUnknown = errors.New("unknown key")

// set takes one top-level key from the file.
func (c *Config) set(key string, value any) error {
	if key == listenKey {
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("`%s` takes a string, not %s", key, kind(value))
		}
		c.Listen = text
		return nil
	}
	for _, n := range numbers {
		if n.key != key {
			continue
		}
		whole, ok := value.(int64)
		switch {
		case !ok:
			return fmt.Errorf("`%s` takes a whole number, not %s", key, kind(value))
		case whole < 0:
			return fmt.Errorf("`%s` takes a whole number, and %d is negative", key, whole)
		case whole > n.max:
			return fmt.Errorf("`%s` takes a whole number no larger than %d", key, n.max)
		}
		n.set(c, whole)
		return nil
	}
	return errUnknown
}

// kind names a decoded TOML value's type as a person would.
func kind(value any) string {
	switch value.(type) {
	case string:
		return "a string"
	case int64:
		return "a whole number"
	case float64:
		return "a fraction"
	case bool:
		return "true or false"
	case map[string]any:
		return "a table"
	case []any, []map[string]any:
		return "an array"
	default:
		return "a date or time"
	}
}

// find is the line that first sets top: a key under it, or a table in it.
func find(file []line, top string) line {
	for _, l := range file {
		if len(l.path) > 0 && l.path[0] == top {
			return l
		}
	}
	return line{}
}
