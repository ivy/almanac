package github_test

import (
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/github"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/hostile/reveal"
	"github.com/ivy/almanac/internal/stall/stalltest"
	"github.com/ivy/almanac/simforge"
)

func fullNames(items []map[string]any) []string {
	var names []string
	for _, item := range items {
		names = append(names, item["full_name"].(string))
	}
	return names
}

func tokensMinted(sim *simforge.Forge) int {
	n := 0
	for _, w := range sim.Writes() {
		if strings.Contains(w.Path, "access_tokens") {
			n++
		}
	}
	return n
}

var _ = Describe("The GitHub client", func() {
	Describe("discovery", func() {
		// crates/github/tests/client.rs: discovery_skips_archived_repositories_and_other_owners
		It("skips archived repositories and other owners", func(ctx SpecContext) {
			sim := simforge.Start()
			DeferCleanup(sim.Close)
			sim.AddRepo(simforge.PublicRepo("owner/live"))
			private := simforge.PublicRepo("owner/private")
			private.Private = true
			sim.AddRepo(private)
			old := simforge.PublicRepo("owner/old")
			old.Archived = true
			sim.AddRepo(old)
			sim.AddRepo(simforge.PublicRepo("someone/else"))

			found, err := client(sim).Repositories(ctx, "owner")
			Expect(err).NotTo(HaveOccurred())
			Expect(fullNames(decodedAll(found))).To(Equal([]string{"owner/live", "owner/private"}))
		})

		// crates/github/tests/client.rs: an_app_finds_every_repository_past_the_first_page
		It("finds every repository an App can reach, past the first page", func(ctx SpecContext) {
			sim := simforge.Start()
			DeferCleanup(sim.Close)
			for i := range 130 {
				sim.AddRepo(simforge.PublicRepo(fmt.Sprintf("owner/repo-%d", i)))
			}
			c := github.New(sim.URL(), app(), github.Retrying(quick()))
			found, err := c.Repositories(ctx, "owner")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(HaveLen(130))
		})

		// crates/github/tests/client.rs: pull_requests_are_listed_by_author_across_pages
		It("lists pull requests by author across pages", func(ctx SpecContext) {
			sim := simforge.Start()
			DeferCleanup(sim.Close)
			sim.AddRepo(simforge.PublicRepo(repo))
			for i := range 120 {
				sim.AddPull(repo, simforge.RenovatePull(fmt.Sprintf("renovate/p%d", i), "Update"))
			}
			human := simforge.RenovatePull("feature", "A person's change")
			human.Author = "someone"
			sim.AddPull(repo, human)

			pulls, err := client(sim).PullsBy(ctx, slug, simforge.Renovate)
			Expect(err).NotTo(HaveOccurred())
			Expect(pulls).To(HaveLen(120))
		})

		It("finds the open pull request for a branch of the repository itself", func(ctx SpecContext) {
			sim, number := scripted()
			found, ok, err := client(sim).PullForBranch(ctx, slug, hostile.MustParseSlug(branch))
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(decoded(found)["number"]).To(BeEquivalentTo(number))

			_, ok, err = client(sim).PullForBranch(ctx, slug, hostile.MustParseSlug("renovate/none"))
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeFalse())
		})
	})

	Describe("retries and refusals", func() {
		// crates/github/tests/client.rs: a_429_backs_off_and_succeeds
		It("waits out a 429 as asked, then succeeds", func(ctx SpecContext) {
			sim, number := scripted()
			sim.FailRequests("/pulls/", simforge.RateLimited(time.Second))
			started := time.Now()
			pull, err := client(sim).Pull(ctx, slug, number)
			Expect(err).NotTo(HaveOccurred())
			Expect(decoded(pull)["number"]).To(BeEquivalentTo(number))
			Expect(time.Since(started)).To(BeNumerically(">=", time.Second), "it must wait as asked")
		})

		// crates/github/tests/client.rs: a_long_rate_limit_is_reported_and_not_waited_out
		It("reports a long rate limit instead of waiting it out", func(ctx SpecContext) {
			sim, number := scripted()
			sim.FailRequests("/pulls/", simforge.RateLimited(time.Hour))
			_, err := client(sim).Pull(ctx, slug, number)
			Expect(err).To(haveCode(diag.ForgeRateLimited))
			Expect(err).To(MatchError(ContainSubstring("resets in 3600s")))
		})

		// crates/github/tests/client.rs: server_errors_are_retried_then_reported
		It("retries server errors, then reports the forge unreachable", func(ctx SpecContext) {
			sim, number := scripted()
			sim.FailRequests("/pulls/", simforge.ServerError(502))
			_, err := client(sim).Pull(ctx, slug, number)
			Expect(err).NotTo(HaveOccurred())

			sim.FailRequests("/pulls/", simforge.ServerError(503), simforge.ServerError(503), simforge.ServerError(503), simforge.ServerError(503))
			_, err = client(sim).Pull(ctx, slug, number)
			Expect(err).To(haveCode(diag.ForgeUnreachable))
			Expect(err).To(MatchError(ContainSubstring("503")))
		})

		// crates/github/tests/client.rs: a_refusal_is_not_retried
		It("does not retry a refusal, and keeps its status", func(ctx SpecContext) {
			sim, _ := scripted()
			_, err := client(sim).Pull(ctx, slug, 999)
			Expect(err).To(haveCode(diag.ForgeRefused))
			var refusal *github.Refusal
			Expect(errors.As(err, &refusal)).To(BeTrue())
			Expect(refusal.Status).To(Equal(404))
		})

		// crates/github/tests/client.rs: a_forge_that_stops_answering_is_unreachable_rather_than_a_hang
		It("reports a forge that stops answering as unreachable, rather than hanging", func(ctx SpecContext) {
			c := github.New(stalltest.Silent(), github.Token("token"), github.Retrying(quick()), github.StallAfter(50*time.Millisecond))
			_, err := c.Exists(ctx, slug, ".github/workflows")
			Expect(err).To(haveCode(diag.ForgeUnreachable))
		}, SpecTimeout(10*time.Second))

		// crates/github/tests/client.rs: a_read_only_client_refuses_every_write
		It("refuses every write when read-only, and sends none", func(ctx SpecContext) {
			sim, number := scripted()
			c := client(sim, github.ReadOnly())
			_, err := c.Pull(ctx, slug, number)
			Expect(err).NotTo(HaveOccurred())

			Expect(c.Post(ctx, "/repos/owner/repo/issues", map[string]any{})).To(haveCode(diag.ForgeReadOnly))
			Expect(c.Patch(ctx, "/repos/owner/repo/git/refs/heads/x", map[string]any{})).To(haveCode(diag.ForgeReadOnly))
			Expect(github.NewForge(c, github.DefaultAuthor()).Attest(ctx, "k", attest(sim, number))).To(haveCode(diag.ForgeReadOnly))
			Expect(sim.Writes()).To(BeEmpty())
		})
	})

	Describe("reads", func() {
		// crates/github/tests/client.rs: mergeable_is_read_again_until_github_has_computed_it
		It("reads mergeable again until GitHub has computed it", func(ctx SpecContext) {
			sim, number := scripted()
			c := client(sim)
			sim.MergeableAfter(2)
			pull, err := c.PullSettled(ctx, slug, number, 5, time.Millisecond)
			Expect(err).NotTo(HaveOccurred())
			Expect(decoded(pull)["mergeable"]).To(BeTrue())

			sim.MergeableAfter(9)
			pull, err = c.PullSettled(ctx, slug, number, 2, time.Millisecond)
			Expect(err).NotTo(HaveOccurred())
			Expect(decoded(pull)).To(HaveKeyWithValue("mergeable", BeNil()), "still unknown means waiting")
		})

		// crates/github/tests/client.rs: checks_statuses_and_logs_can_be_read
		It("reads checks, statuses and logs", func(ctx SpecContext) {
			sim, number := scripted()
			sha := hostile.MustParseSha(sim.Pull(repo, int(number)).HeadSHA)
			sim.AddSuite(repo, simforge.FinishedSuite(sha.String(), branch, "failure", "error: boom"))
			c := client(sim)

			suites, err := c.CheckSuites(ctx, slug, sha)
			Expect(err).NotTo(HaveOccurred())
			Expect(decoded(suites)["check_suites"]).To(ContainElement(HaveKeyWithValue("conclusion", "failure")))

			runs, err := c.CheckRuns(ctx, slug, sha)
			Expect(err).NotTo(HaveOccurred())
			job := decoded(runs)["check_runs"].([]any)[0].(map[string]any)["id"].(float64)
			logs, err := c.JobLogs(ctx, slug, uint64(job))
			Expect(err).NotTo(HaveOccurred())
			Expect(reveal.Value(logs)).To(Equal("error: boom"))

			status, err := c.CombinedStatus(ctx, slug, sha)
			Expect(err).NotTo(HaveOccurred())
			Expect(decoded(status)["state"]).To(Equal("pending"))
		})

		// crates/github/tests/client.rs: a_pull_requests_added_lines_are_read_from_its_diffs
		It("reads a pull request's added lines from its diffs", func(ctx SpecContext) {
			sim := simforge.Start()
			DeferCleanup(sim.Close)
			sim.AddRepo(simforge.PublicRepo(repo))
			pull := simforge.RenovatePull("renovate/x", "Update")
			pull.Files = []string{"package.json", "big.lock"}
			pull.Diffs = map[string]string{"package.json": "@@ -1 +1 @@\n-old\n+new"}
			number := uint64(sim.AddPull(repo, pull))
			c := github.New(sim.URL(), github.Token("t"))

			added, err := c.PullAddedLines(ctx, slug, number)
			Expect(err).NotTo(HaveOccurred())
			Expect(reveal.Value(added)).To(Equal([]github.AddedLine{{Path: "package.json", Line: "new"}}))

			files, err := c.PullFiles(ctx, slug, number)
			Expect(err).NotTo(HaveOccurred())
			Expect(reveal.Value(files)).To(Equal([]string{"package.json", "big.lock"}))
		})

		It("reads a pull request's commit messages, oldest first", func(ctx SpecContext) {
			sim := simforge.Start()
			DeferCleanup(sim.Close)
			sim.AddRepo(simforge.PublicRepo(repo))
			pull := simforge.RenovatePull(branch, "Update")
			pull.Commits = []string{"chore(deps): update lodash", "fix: repair\n\nAlmanac-Key: k"}
			number := uint64(sim.AddPull(repo, pull))

			messages, err := client(sim).PullCommitMessages(ctx, slug, number)
			Expect(err).NotTo(HaveOccurred())
			Expect(reveal.Value(messages)).To(Equal(pull.Commits))
		})

		It("reads a file's text, and a missing file as absent", func(ctx SpecContext) {
			sim := simforge.Start()
			DeferCleanup(sim.Close)
			configured := simforge.PublicRepo(repo)
			configured.Renovate = `{"extends": ["config:recommended"]}`
			sim.AddRepo(configured)
			c := client(sim)

			text, found, err := c.File(ctx, slug, "renovate.json")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(reveal.Value(text)).To(Equal(configured.Renovate))

			_, found, err = c.File(ctx, slug, "renovate.json5")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
		})

		It("reads the status checks a branch requires", func(ctx SpecContext) {
			sim := simforge.Start()
			DeferCleanup(sim.Close)
			protected := simforge.PublicRepo(repo)
			protected.RequiredChecks = []string{"ci/build", "almanac/trust"}
			sim.AddRepo(protected)

			checks, err := client(sim).RequiredChecks(ctx, slug, hostile.MustParseSlug("main"))
			Expect(err).NotTo(HaveOccurred())
			Expect(reveal.Value(checks)).To(Equal(protected.RequiredChecks))
		})

		It("downloads a commit's tarball as it is served", func(ctx SpecContext) {
			sim, number := scripted()
			sim.SetTarball(repo, []byte("gzipped tar"))
			sha := hostile.MustParseSha(sim.Pull(repo, int(number)).HeadSHA)
			tarball, err := client(sim).Tarball(ctx, slug, sha)
			Expect(err).NotTo(HaveOccurred())
			Expect(tarball).To(Equal([]byte("gzipped tar")))
		})

		// crates/github/tests/tracker.rs: a_directory_exists_though_it_has_no_content
		It("finds that a directory exists though it has no content", func(ctx SpecContext) {
			recorded, url := newRecorder(200, `[{"name": "ci.yml"}]`)
			c := github.New(url, github.Token("t"))
			Expect(c.Exists(ctx, slug, ".github/workflows")).To(BeTrue())
			Expect(recorded.headers()).To(HaveLen(1))
			Expect(recorded.path(0)).To(Equal("/repos/owner/repo/contents/.github/workflows"))

			sim, _ := scripted()
			Expect(client(sim).Exists(ctx, slug, "missing")).To(BeFalse())
		})
	})

	Describe("deliveries", func() {
		// crates/github/tests/client.rs: deliveries_can_be_listed_and_redelivered
		It("lists deliveries and redelivers one", func(ctx SpecContext) {
			sim, number := scripted()
			sim.NextDeliveries(simforge.Lost)
			sim.PullRequest(ctx, repo, int(number), "opened")
			c := client(sim)

			listed, err := c.Deliveries(ctx)
			Expect(err).NotTo(HaveOccurred())
			first := decodedAll(listed)[0]
			Expect(first["status_code"]).To(BeEquivalentTo(502))
			Expect(c.Redeliver(ctx, uint64(first["id"].(float64)))).To(Succeed())
		})

		// crates/github/tests/client.rs: a_sweep_redelivers_what_failed_and_only_once
		It("sweeps up what failed, and asks for it only once", func(ctx SpecContext) {
			sim, number := scripted()
			received := newReceiver()
			sim.DeliverTo(received.url, "s")
			sim.NextDeliveries(simforge.Lost)
			sim.PullRequest(ctx, repo, int(number), "opened")
			sim.PullRequest(ctx, repo, int(number), "edited")
			c := client(sim)

			Expect(c.Sweep(ctx)).To(Equal(1))
			Expect(c.Sweep(ctx)).To(Equal(0), "it succeeded the second time")
			Expect(received.count()).To(Equal(2))
		})
	})

	Describe("App authentication", func() {
		// crates/github/tests/client.rs: an_app_authenticates_with_a_signed_token_then_an_installation_token
		It("signs a token as the App, then uses an installation token it keeps", func(ctx SpecContext) {
			sim, _ := scripted()
			c := github.New(sim.URL(), app(), github.Retrying(quick()))
			found, err := c.Repositories(ctx, "owner")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(HaveLen(1))
			_, err = c.Repositories(ctx, "owner")
			Expect(err).NotTo(HaveOccurred())
			Expect(tokensMinted(sim)).To(Equal(1), "the token is cached")
		})

		// crates/github/tests/client.rs: a_private_key_may_arrive_on_one_line
		It("takes a private key that arrives on one line", func(ctx SpecContext) {
			sim, _ := scripted()
			oneLine := app()
			oneLine.PrivateKey = strings.ReplaceAll(oneLine.PrivateKey, "\n", `\n`)
			c := github.New(sim.URL(), oneLine, github.Retrying(quick()))
			Expect(c.Repositories(ctx, "owner")).To(HaveLen(1))
		})

		It("calls /app endpoints with the App's own token, signed RS256 and short-lived", func(ctx SpecContext) {
			recorded, url := newRecorder(200, `[]`)
			c := github.New(url, app())
			Expect(c.Deliveries(ctx)).To(BeEmpty())

			bearer, ok := strings.CutPrefix(recorded.headers()[0].Get("Authorization"), "Bearer ")
			Expect(ok).To(BeTrue())
			key, _ := privateKey()
			token, err := jwt.ParseWithClaims(bearer, &jwt.RegisteredClaims{}, func(*jwt.Token) (any, error) {
				return &key.PublicKey, nil
			}, jwt.WithValidMethods([]string{"RS256"}))
			Expect(err).NotTo(HaveOccurred())
			claims := token.Claims.(*jwt.RegisteredClaims)
			Expect(claims.Issuer).To(Equal("1"))
			Expect(claims.ExpiresAt.Sub(claims.IssuedAt.Time)).To(BeNumerically("<=", 10*time.Minute))
			Expect(claims.IssuedAt.Time).To(BeTemporally("<", time.Now()))
		})

		It("reports a key it cannot read, without sending anything", func(ctx SpecContext) {
			sim, _ := scripted()
			broken := app()
			broken.PrivateKey = "not a key"
			_, err := github.New(sim.URL(), broken).Repositories(ctx, "owner")
			Expect(err).To(haveCode(diag.ForgeUnreachable))
			Expect(err).To(MatchError(ContainSubstring("signing an App token")))
			Expect(sim.Writes()).To(BeEmpty())
		})
	})

	It("sends the API version, and decodes the base64 GitHub wraps", func(ctx SpecContext) {
		content := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 100)))
		wrapped := strings.Join(slices.Collect(chunks(content, 60)), `\n`)
		recorded, url := newRecorder(200, `{"content": "`+wrapped+`"}`)
		text, found, err := github.New(url, github.Token("t")).File(ctx, slug, "renovate.json")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(reveal.Value(text)).To(Equal(strings.Repeat("x", 100)))
		Expect(recorded.headers()[0].Get("X-GitHub-Api-Version")).To(Equal("2022-11-28"))
		Expect(recorded.headers()[0].Get("Authorization")).To(Equal("Bearer t"))
	})
})

func chunks(text string, size int) func(func(string) bool) {
	return func(yield func(string) bool) {
		for len(text) > 0 {
			n := min(size, len(text))
			if !yield(text[:n]) {
				return
			}
			text = text[n:]
		}
	}
}
