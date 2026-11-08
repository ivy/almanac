package broker_test

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/broker"
	"github.com/ivy/almanac/diag"
)

// record writes a recorded delivery file into dir.
func record(dir, name, event, id string, body []byte) {
	GinkgoHelper()
	content := "X-GitHub-Event: " + event + "\nX-GitHub-Delivery: " + id + "\n\n" + string(body)
	Expect(os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)).To(Succeed())
}

var _ = Describe("A recorded delivery", func() {
	var dir string

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
	})

	It("is read with its body exactly as written, trailing newline and all", func() {
		exact := []byte("{\"action\": \"opened\"}\n\n")
		record(dir, "1.delivery", "pull_request", "d1", exact)
		recorded, err := broker.ReadRecorded(filepath.Join(dir, "1.delivery"))
		Expect(err).NotTo(HaveOccurred())
		Expect(recorded).To(Equal(broker.Recorded{Event: "pull_request", Delivery: "d1", Body: exact}))
	})

	It("takes header names in any case and lines ending in CRLF", func() {
		content := "x-github-event: status\r\nx-github-delivery: d2\r\n\r\n{}"
		path := filepath.Join(dir, "2.delivery")
		Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
		recorded, err := broker.ReadRecorded(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(recorded).To(Equal(broker.Recorded{Event: "status", Delivery: "d2", Body: []byte("{}")}))
	})

	DescribeTable("is refused with what is wrong and where",
		func(content, reason string) {
			path := filepath.Join(dir, "bad.delivery")
			Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
			_, err := broker.ReadRecorded(path)
			Expect(err).To(MatchError(And(ContainSubstring(path), ContainSubstring(reason))))
		},
		Entry("with no blank line", "X-GitHub-Event: status\n", "do not end in a blank line"),
		Entry("with a missing header", "X-GitHub-Event: status\n\n{}", "needs both"),
		Entry("with a signature", "X-GitHub-Event: status\nX-GitHub-Delivery: d\nX-Hub-Signature-256: sha256=00\n\n{}", "X-Hub-Signature-256"),
	)

	It("that cannot be read says so with a code", func() {
		_, err := broker.ReadRecorded(filepath.Join(dir, "missing.delivery"))
		Expect(err).To(BeAssignableToTypeOf(&diag.Error{}))
		Expect(err.(*diag.Error).Code).To(Equal(diag.InputUnreadable))
	})
})

var _ = Describe("Replaying recorded deliveries", func() {
	var (
		dir string
		log *memoryLog
		url string
		out bytes.Buffer
	)

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		log = &memoryLog{}
		url = serving(log, GinkgoWriter) + "/webhooks/github"
		out.Reset()
	})

	replay := func(secret string) error {
		return broker.Replay(context.Background(), http.DefaultClient, dir, url, secret, &out)
	}

	It("signs each one, posts it in name order, and the broker logs it as received", func() {
		opened := []byte(`{"action":"opened","number":1}`)
		failed := []byte(`{"state":"failure"}`)
		record(dir, "000002-b.delivery", "status", "b", failed)
		record(dir, "000001-a.delivery", "pull_request", "a", opened)
		Expect(os.WriteFile(filepath.Join(dir, "README"), []byte("not a delivery"), 0o600)).To(Succeed())

		Expect(replay(secret)).To(Succeed())

		logged := log.all()
		Expect(logged).To(HaveLen(2))
		Expect(logged[0].DeliveryID).To(Equal("a"))
		Expect(logged[0].Kind).To(Equal("github.pull_request.opened"))
		Expect(logged[0].Body).To(Equal(opened))
		Expect(logged[1].Kind).To(Equal("github.status"))
		Expect(logged[1].Body).To(Equal(failed))
		Expect(out.String()).To(Equal("000001-a.delivery: 202 Accepted\n000002-b.delivery: 202 Accepted\n"))
	})

	It("is harmless twice, because the broker logs a delivery once", func() {
		record(dir, "a.delivery", "pull_request", "a", body)
		Expect(replay(secret)).To(Succeed())
		Expect(replay(secret)).To(Succeed())
		Expect(log.all()).To(HaveLen(1))
	})

	It("with the wrong secret fails, and says what a 401 means", func() {
		record(dir, "a.delivery", "pull_request", "a", body)
		err := replay("not the broker's secret")
		Expect(err).To(MatchError(ContainSubstring("refused 1 of 1")))
		Expect(err).To(MatchError(ContainSubstring("ALMANAC_WEBHOOK_SECRET")))
		Expect(out.String()).To(Equal("a.delivery: 401 Unauthorized\n"))
		Expect(log.all()).To(BeEmpty())
	})

	It("of a directory with nothing recorded fails rather than doing nothing", func() {
		Expect(replay(secret)).To(MatchError(ContainSubstring("nothing to replay")))
	})
})
