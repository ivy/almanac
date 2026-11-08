package deliveries_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/deliveries"
	"github.com/ivy/almanac/diag"
)

// connect is the log in a database of the spec's own.
func connect(ctx context.Context) (*deliveries.Log, string) {
	GinkgoHelper()
	url := database()
	log, err := deliveries.Connect(ctx, url)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(log.Close)
	return log, url
}

func delivery(id string) deliveries.Delivery {
	return deliveries.Delivery{Source: "github", DeliveryID: id, Kind: "github.pull_request.opened", Body: []byte(`{"action":"opened"}`)}
}

func appended(ctx context.Context, log *deliveries.Log, ids ...string) {
	GinkgoHelper()
	for _, id := range ids {
		Expect(log.Append(ctx, delivery(id))).To(BeTrue())
	}
}

// taking records what a handler took.
type taking struct {
	mu   sync.Mutex
	took []string
}

func (t *taking) handle(_ context.Context, d deliveries.Logged) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.took = append(t.took, d.DeliveryID)
	return nil
}

func (t *taking) ids() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.took...)
}

// settling is a settle that returns err, and counts its calls.
func settling(err error) (func(context.Context) error, *atomic.Int32) {
	var calls atomic.Int32
	return func(context.Context) error {
		calls.Add(1)
		return err
	}, &calls
}

var _ = Describe("The deliveries log", Label("db"), func() {
	It("keeps a delivery once, with its exact body, however often it is appended", func(ctx SpecContext) {
		log, _ := connect(ctx)
		d := delivery("d1")
		d.Body = []byte("{\"action\":\"opened\"}\n\x00\xff")
		Expect(log.Append(ctx, d)).To(BeTrue())
		Expect(log.Append(ctx, d)).To(BeFalse())
		logged, err := log.After(ctx, 0, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(logged).To(HaveLen(1))
		Expect(logged[0].Delivery).To(Equal(d))
	})

	It("is migrated by any number of processes at once", func(ctx SpecContext) {
		url := database()
		var wg sync.WaitGroup
		errs := make(chan error, 4)
		for range 4 {
			wg.Go(func() {
				log, err := deliveries.Connect(ctx, url)
				if err == nil {
					log.Close()
				}
				errs <- err
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			Expect(err).NotTo(HaveOccurred())
		}
	})

	It("that cannot be reached says so with the store's code", func(ctx SpecContext) {
		_, err := deliveries.Connect(ctx, "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
		var d *diag.Error
		Expect(errors.As(err, &d)).To(BeTrue())
		Expect(d.Code).To(Equal(diag.StoreFailed))
	})
})

var _ = Describe("A cursor", Label("db"), func() {
	It("of a new almanac starts at the end of the log", func(ctx SpecContext) {
		log, _ := connect(ctx)
		appended(ctx, log, "old")
		cursor, err := log.Cursor(ctx, "new")
		Expect(err).NotTo(HaveOccurred())
		var t taking
		Expect(cursor.CatchUp(ctx, t.handle)).To(Equal(0))
		appended(ctx, log, "next")
		Expect(cursor.CatchUp(ctx, t.handle)).To(Equal(1))
		Expect(t.ids()).To(Equal([]string{"next"}))
	})

	It("of an almanac that was stopped carries on from where it stopped", func(ctx SpecContext) {
		log, url := connect(ctx)
		_, err := log.Cursor(ctx, "b")
		Expect(err).NotTo(HaveOccurred())
		log.Close()

		other, err := deliveries.Connect(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(other.Close)
		appended(ctx, other, "a", "b", "c")

		cursor, err := other.Cursor(ctx, "b")
		Expect(err).NotTo(HaveOccurred())
		var t taking
		Expect(cursor.CatchUp(ctx, t.handle)).To(Equal(3))
		Expect(t.ids()).To(Equal([]string{"a", "b", "c"}))
		Expect(cursor.CatchUp(ctx, t.handle)).To(Equal(0), "each delivery is passed once")
	})

	It("is each almanac's own", func(ctx SpecContext) {
		log, _ := connect(ctx)
		first, err := log.Cursor(ctx, "first")
		Expect(err).NotTo(HaveOccurred())
		second, err := log.Cursor(ctx, "second")
		Expect(err).NotTo(HaveOccurred())
		appended(ctx, log, "d1")
		var t taking
		Expect(first.CatchUp(ctx, t.handle)).To(Equal(1))
		Expect(second.Seq(ctx)).To(BeZero())
		Expect(second.CatchUp(ctx, t.handle)).To(Equal(1))
		Expect(t.ids()).To(Equal([]string{"d1", "d1"}))
	})

	It("a body that is not JSON cannot stop a cursor", func(ctx SpecContext) {
		log, _ := connect(ctx)
		cursor, err := log.Cursor(ctx, "a")
		Expect(err).NotTo(HaveOccurred())
		garbage := delivery("garbage")
		garbage.Body = []byte("not json")
		Expect(log.Append(ctx, garbage)).To(BeTrue())
		appended(ctx, log, "after")
		var t taking
		Expect(cursor.CatchUp(ctx, t.handle)).To(Equal(2))
		Expect(t.ids()).To(Equal([]string{"after"}))
	})

	It("stays before a delivery its handler fails on, so it is handed over again", func(ctx SpecContext) {
		log, _ := connect(ctx)
		cursor, err := log.Cursor(ctx, "a")
		Expect(err).NotTo(HaveOccurred())
		appended(ctx, log, "d1", "d2")
		failing := errors.New("the store is down")
		var t taking
		handle := func(ctx context.Context, d deliveries.Logged) error {
			if d.DeliveryID == "d2" {
				return failing
			}
			return t.handle(ctx, d)
		}
		passed, err := cursor.CatchUp(ctx, handle)
		Expect(err).To(MatchError(failing))
		Expect(passed).To(Equal(1))
		Expect(cursor.CatchUp(ctx, t.handle)).To(Equal(1))
		Expect(t.ids()).To(Equal([]string{"d1", "d2"}))
	})

	It("following wakes on notify without waiting for a poll", func(ctx SpecContext) {
		log, _ := connect(ctx)
		cursor, err := log.Cursor(ctx, "a")
		Expect(err).NotTo(HaveOccurred())
		cursor.Poll = time.Hour
		var t taking
		following, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- cursor.Follow(following, t.handle, nil) }()
		DeferCleanup(func() {
			stop()
			Eventually(done).Should(Receive(BeNil()))
		})

		time.Sleep(200 * time.Millisecond)
		appended(ctx, log, "d1")
		Eventually(t.ids).WithTimeout(2*time.Second).Should(Equal([]string{"d1"}), "the notification was not acted on")
		Expect(cursor.Seq(ctx)).To(BeNumerically(">", 0))
	})

	It("following finds a delivery whose notification was lost at the next poll", func(ctx SpecContext) {
		log, url := connect(ctx)
		cursor, err := log.Cursor(ctx, "a")
		Expect(err).NotTo(HaveOccurred())
		cursor.Poll = 100 * time.Millisecond
		settle, settled := settling(nil)
		var t taking
		following, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- cursor.Follow(following, t.handle, settle) }()
		DeferCleanup(func() {
			stop()
			Eventually(done).Should(Receive(BeNil()))
		})

		// Once the follower has caught up and is waiting, an insert that
		// notifies nobody, as a lost notification would.
		Eventually(settled.Load).Should(BeNumerically(">=", 1))
		pool, err := pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(pool.Close)
		_, err = pool.Exec(ctx, `INSERT INTO deliveries (source, delivery_id, kind, body) VALUES ('github', 'quiet', 'none', '{}')`)
		Expect(err).NotTo(HaveOccurred())
		Eventually(t.ids).WithTimeout(2 * time.Second).Should(Equal([]string{"quiet"}))
		Eventually(settled.Load).Should(BeNumerically(">=", 2), "settle runs after every catch-up")
		Expect(t.ids()).To(HaveLen(1))
	})

	It("following stops at the first error from settle, and returns it", func(ctx SpecContext) {
		log, _ := connect(ctx)
		cursor, err := log.Cursor(ctx, "a")
		Expect(err).NotTo(HaveOccurred())
		failing := errors.New("the runtime cannot settle")
		settle, settled := settling(failing)
		var t taking
		Expect(cursor.Follow(ctx, t.handle, settle)).To(MatchError(failing))
		Expect(settled.Load()).To(BeEquivalentTo(1))
	})

	It("a reader racing concurrent appends misses nothing", func(ctx SpecContext) {
		log, _ := connect(ctx)
		cursor, err := log.Cursor(ctx, "a")
		Expect(err).NotTo(HaveOccurred())
		const total = 200
		var writers sync.WaitGroup
		for i := range total {
			writers.Go(func() {
				defer GinkgoRecover()
				Expect(log.Append(ctx, delivery(fmt.Sprintf("d%d", i)))).To(BeTrue())
			})
		}
		var t taking
		read := 0
		deadline := time.Now().Add(30 * time.Second)
		for read < total && time.Now().Before(deadline) {
			n, err := cursor.CatchUp(ctx, t.handle)
			Expect(err).NotTo(HaveOccurred())
			read += n
		}
		writers.Wait()
		Expect(read).To(Equal(total), "a delivery was skipped")
		Expect(t.ids()).To(HaveLen(total))
	}, SpecTimeout(time.Minute))
})
