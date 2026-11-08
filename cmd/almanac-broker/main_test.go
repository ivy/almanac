package main_test

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"

	"github.com/ivy/almanac/deliveries"
	"github.com/ivy/almanac/simforge"
)

const secret = "a webhook secret"

// run starts almanac-broker with args and nothing in its environment but env.
func run(env []string, args ...string) *gexec.Session {
	GinkgoHelper()
	cmd := exec.Command(binary, args...)
	cmd.Env = env
	session, err := gexec.Start(cmd, GinkgoWriter, GinkgoWriter)
	Expect(err).NotTo(HaveOccurred())
	return session
}

// freeAddress is a loopback address nothing listens on.
func freeAddress() string {
	GinkgoHelper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	address := listener.Addr().String()
	Expect(listener.Close()).To(Succeed())
	return address
}

func post(url, event, id string, body []byte, signature string) int {
	GinkgoHelper()
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	Expect(err).NotTo(HaveOccurred())
	request.Header.Set("X-GitHub-Event", event)
	request.Header.Set("X-GitHub-Delivery", id)
	if signature != "" {
		request.Header.Set("X-Hub-Signature-256", signature)
	}
	response, err := http.DefaultClient.Do(request)
	Expect(err).NotTo(HaveOccurred())
	Expect(response.Body.Close()).To(Succeed())
	return response.StatusCode
}

var _ = Describe("almanac-broker", func() {
	It("refuses to listen anywhere but loopback", func() {
		session := run([]string{
			"ALMANAC_WEBHOOK_SECRET=" + secret,
			"DATABASE_URL=postgres://unused",
			"ALMANAC_BROKER_LISTEN=0.0.0.0:8789",
		})
		Eventually(session).Should(gexec.Exit(1))
		Expect(session.Err).To(gbytes.Say(`ALMANAC_BROKER_LISTEN=0.0.0.0:8789 is not a loopback address`))
	})

	It("without a secret names what to set, under a code", func() {
		session := run([]string{"DATABASE_URL=postgres://unused"})
		Eventually(session).Should(gexec.Exit(1))
		Expect(session.Err).To(gbytes.Say(`almanac::config::env_missing`))
		Expect(session.Err).To(gbytes.Say(`ALMANAC_WEBHOOK_SECRET is not set`))
	})

	It("documents the replay format in its help", func() {
		session := run(nil, "replay", "--help")
		Eventually(session).Should(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say(`X-GitHub-Event: pull_request`))
		Expect(session.Out).To(gbytes.Say(`X-GitHub-Delivery: `))
	})

	Context("serving", Label("db"), func() {
		var (
			url  string
			hook string
			log  *deliveries.Log
		)

		BeforeEach(func(ctx SpecContext) {
			databaseURL := database()
			address := freeAddress()
			url = "http://" + address
			hook = url + "/webhooks/github"
			session := run([]string{
				"ALMANAC_WEBHOOK_SECRET=" + secret,
				"DATABASE_URL=" + databaseURL,
				"ALMANAC_BROKER_LISTEN=" + address,
			})
			DeferCleanup(func() {
				session.Terminate()
				Eventually(session).WithTimeout(10*time.Second).Should(gexec.Exit(0), "it stops cleanly on SIGTERM")
			})
			Eventually(func() (int, error) {
				response, err := http.Get(url + "/readyz")
				if err != nil {
					return 0, err
				}
				return response.StatusCode, response.Body.Close()
			}).WithTimeout(30 * time.Second).Should(Equal(http.StatusOK))

			var err error
			log, err = deliveries.Connect(ctx, databaseURL)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(log.Close)
		})

		logged := func(ctx context.Context) []deliveries.Logged {
			GinkgoHelper()
			all, err := log.After(ctx, 0, 100)
			Expect(err).NotTo(HaveOccurred())
			return all
		}

		It("logs a delivery signed with ALMANAC_WEBHOOK_SECRET and refuses one that is not", func(ctx SpecContext) {
			body := []byte(`{"action":"opened","number":1}`)
			Expect(post(hook, "pull_request", "unsigned", body, "")).To(Equal(http.StatusUnauthorized))
			Expect(post(hook, "pull_request", "wrong", body, simforge.Sign("not the secret", body))).To(Equal(http.StatusUnauthorized))
			Expect(logged(ctx)).To(BeEmpty())

			Expect(post(hook, "pull_request", "signed", body, simforge.Sign(secret, body))).To(Equal(http.StatusAccepted))
			Expect(logged(ctx)).To(ConsistOf(HaveField("Delivery", deliveries.Delivery{
				Source: "github", DeliveryID: "signed", Kind: "github.pull_request.opened", Body: body,
			})))
		})

		It("replay feeds recorded deliveries to it, signed with the secret from the environment", func(ctx SpecContext) {
			dir := GinkgoT().TempDir()
			opened := []byte("{\"action\": \"opened\",\n \"number\": 7}\n")
			Expect(os.WriteFile(filepath.Join(dir, "000001-a.delivery"),
				append([]byte("X-GitHub-Event: pull_request\nX-GitHub-Delivery: a\n\n"), opened...), 0o600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dir, "000002-b.delivery"),
				[]byte("X-GitHub-Event: status\nX-GitHub-Delivery: b\n\n{\"state\":\"failure\"}"), 0o600)).To(Succeed())

			replay := run([]string{"ALMANAC_WEBHOOK_SECRET=" + secret}, "replay", dir, "--url", hook)
			Eventually(replay).Should(gexec.Exit(0))
			Expect(replay.Out).To(gbytes.Say(`000001-a.delivery: 202 Accepted`))
			Expect(replay.Out).To(gbytes.Say(`000002-b.delivery: 202 Accepted`))

			all := logged(ctx)
			Expect(all).To(HaveLen(2))
			Expect(all[0].Kind).To(Equal("github.pull_request.opened"))
			Expect(all[0].Body).To(Equal(opened))
			Expect(all[1].Kind).To(Equal("github.status"))

			again := run([]string{"ALMANAC_WEBHOOK_SECRET=" + secret}, "replay", dir, "--url", hook)
			Eventually(again).Should(gexec.Exit(0))
			Expect(logged(ctx)).To(HaveLen(2), "a replayed delivery is logged once")

			wrong := run([]string{"ALMANAC_WEBHOOK_SECRET=not the secret"}, "replay", dir, "--url", hook)
			Eventually(wrong).Should(gexec.Exit(1))
			Expect(wrong.Err).To(gbytes.Say(`refused 2 of 2`))
		})
	})
})
