// Package pgtest gives specs labelled db a PostgreSQL of their own, started
// through testcontainers on Podman. The Makefile points DOCKER_HOST at
// Podman's socket.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Image is the PostgreSQL the specs run against.
const Image = "docker.io/library/postgres:17-alpine"

// A suite shares one PostgreSQL between its parallel processes, started only
// if a spec labelled db or e2e will run:
//
//	var _ = SynchronizedBeforeSuite(pgtest.Start, pgtest.Use)
//	var _ = SynchronizedAfterSuite(func() {}, pgtest.Stop)
//
// Each spec then calls [Database] for an empty database of its own.
var (
	container *postgres.PostgresContainer
	server    string
)

// Start runs on the suite's first process. It starts PostgreSQL and returns
// its URL for [Use]. Specs labelled db or e2e need it, so it starts only when
// the label filter lets one of them run.
func Start(ctx ginkgo.SpecContext) []byte {
	filter := ginkgo.GinkgoLabelFilter()
	if !ginkgo.Label("db").MatchesLabelFilter(filter) && !ginkgo.Label("e2e").MatchesLabelFilter(filter) {
		return nil
	}
	var err error
	container, err = postgres.Run(ctx, Image, postgres.BasicWaitStrategies())
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "start PostgreSQL through podman")
	return []byte(container.MustConnectionString(ctx, "sslmode=disable"))
}

// Use runs on every process, with what [Start] returned.
func Use(url []byte) {
	server = string(url)
}

// Stop runs on the suite's first process once every other has finished.
func Stop(ctx ginkgo.SpecContext) {
	if container != nil {
		gomega.Expect(container.Terminate(ctx)).To(gomega.Succeed())
	}
}

// Database creates an empty database and returns its URL, so specs never see
// each other's rows.
func Database() string {
	ginkgo.GinkgoHelper()
	gomega.Expect(server).NotTo(gomega.BeEmpty(), "a spec that needs PostgreSQL must carry Label(\"db\") or Label(\"e2e\")")
	return database(server)
}

func database(server string) string {
	ginkgo.GinkgoHelper()
	ctx := context.Background()
	name := "spec_" + randomHex()
	conn, err := pgx.Connect(ctx, server)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, "CREATE DATABASE "+name)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	u, err := url.Parse(server)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	u.Path = "/" + name
	return u.String()
}

func randomHex() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
