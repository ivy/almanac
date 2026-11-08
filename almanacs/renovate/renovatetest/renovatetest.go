// Package renovatetest holds doubles for specs of the Renovate almanac: a package registry held
// in memory, a way to set a commit status on simforge, and the providers a vocabulary reads
// through, pointed at a simforge.
package renovatetest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/ivy/almanac/advisory"
	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/clock"
	"github.com/ivy/almanac/github"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/registry"
	"github.com/ivy/almanac/simforge"
)

// Registry is a package registry held in memory: releases with their publish times and
// archives, by name@version. It records every release it is asked about. It is safe for
// concurrent use.
type Registry struct {
	mu        sync.Mutex
	published map[string]time.Time
	archives  map[string][]byte
	asked     []string
	down      bool
}

// ErrDown is what a Registry that is down answers.
var ErrDown = errors.New("the registry is down")

// NewRegistry is a registry with no releases.
func NewRegistry() *Registry {
	return &Registry{published: map[string]time.Time{}, archives: map[string][]byte{}}
}

// Release publishes name at version at the time at, with an archive of files under one top
// directory, as npm packs one.
func (r *Registry) Release(name, version string, at time.Time, files map[string]string) {
	archive, err := Archive(files)
	if err != nil {
		panic(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.published[name+"@"+version] = at
	r.archives[name+"@"+version] = archive
}

// Replace replaces a release's archive with bytes, such as one that is not an archive at all.
func (r *Registry) Replace(name, version string, archive []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.archives[name+"@"+version] = archive
}

// GoDown makes every later download fail with ErrDown.
func (r *Registry) GoDown() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.down = true
}

// Asked is every release the registry was asked about, as name@version, oldest first.
func (r *Registry) Asked() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.asked)
}

// Published is when the release was published. A release never released is unknown.
func (r *Registry) Published(_ context.Context, p registry.Package) (time.Time, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, p.String())
	at, ok := r.published[p.String()]
	return at, ok, nil
}

// Artifact is the release's archive, or registry.ErrNoArtifact for one never released.
func (r *Registry) Artifact(_ context.Context, p registry.Package) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, p.String())
	if r.down {
		return nil, ErrDown
	}
	archive, ok := r.archives[p.String()]
	if !ok {
		return nil, registry.ErrNoArtifact
	}
	return archive, nil
}

// Archive is a gzipped tar of files under one top directory, "package/", as npm packs one.
func Archive(files map[string]string) ([]byte, error) {
	var out bytes.Buffer
	zipped := gzip.NewWriter(&out)
	archive := tar.NewWriter(zipped)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		content := files[name]
		header := &tar.Header{Name: "package/" + name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := archive.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := archive.Write([]byte(content)); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	if err := zipped.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// SetStatus sets a commit status on the forge, as anything with write access to repo can. It
// sends no webhook: simforge's StatusChanged does, and records no status.
func SetStatus(sim *simforge.Forge, repo, sha, statusContext, state string) error {
	body, err := json.Marshal(map[string]string{"context": statusContext, "state": state})
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, sim.URL()+"/repos/"+repo+"/statuses/"+sha, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	if err := response.Body.Close(); err != nil {
		return err
	}
	if response.StatusCode != http.StatusCreated {
		return fmt.Errorf("simforge answered a status with %d", response.StatusCode)
	}
	return nil
}

// World is what the Renovate vocabulary reads through, in a spec.
type World struct {
	Set        *providers.Set
	Client     *github.Client
	Registry   *Registry
	Advisories *advisory.Fake
	Clock      *clock.Fake
}

// NewWorld provides a client of sim that retries quickly, owner's Renovate pull requests by
// simforge.Renovate as the target, and an empty registry and advisory feed, at the time now.
func NewWorld(sim *simforge.Forge, owner string, now time.Time) *World {
	w := &World{
		Set:        providers.New(),
		Registry:   NewRegistry(),
		Advisories: advisory.NewFake(),
		Clock:      clock.NewFake(now),
	}
	retry := github.Retrying(github.Retry{Attempts: 2, Backoff: time.Millisecond, LongestWait: time.Second})
	w.Client = github.New(sim.URL(), github.Token("token"), retry)
	providers.Provide(w.Set, w.Client)
	providers.Provide(w.Set, renovate.Target{Owner: owner, Author: simforge.Renovate})
	providers.Provide[clock.Clock](w.Set, w.Clock)
	providers.Provide[registry.Registry](w.Set, w.Registry)
	providers.Provide[advisory.Advisories](w.Set, w.Advisories)
	return w
}
