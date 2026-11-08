package postgres_test

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/deliveries"
	"github.com/ivy/almanac/internal/pgtest"
	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/store/postgres"
	"github.com/ivy/almanac/store/storetest"
)

// migrated is a pool on a database of the spec's own, with the store's schema.
func migrated(ctx context.Context) *pgxpool.Pool {
	GinkgoHelper()
	pool, err := pgxpool.New(ctx, pgtest.Database())
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(pool.Close)
	Expect(postgres.Migrate(ctx, pool)).To(Succeed())
	return pool
}

func queue(key, payload string) store.Admission {
	return store.Queue(store.Event{Workflow: "w", Key: key, Payload: json.RawMessage(payload)})
}

var _ = Describe("The PostgreSQL store", Label("db"), func() {
	storetest.Conformance(func(ctx context.Context) storetest.Open {
		pool := migrated(ctx)
		return func(scope string) store.Store { return postgres.New(pool, scope) }
	})

	It("accepts a redelivery of a delivery that failed to persist", func(ctx SpecContext) {
		s := postgres.New(migrated(ctx), "redelivery")
		_, err := s.Admit(ctx, "t", "d1", queue("a key PostgreSQL refuses \x00", "1"))
		Expect(err).To(HaveOccurred())

		Expect(s.Admit(ctx, "t", "d1", queue("k", "1"))).To(Equal(store.Queued))
	})

	It("is itself refused a second active run for a key", func(ctx SpecContext) {
		pool := migrated(ctx)
		insert := `INSERT INTO runs (almanac, workflow, key, payload, origin, depth, status, started_at)
		           VALUES ('a', 'w', 'k', 'null', 'webhook', 0, 'running', now())`
		_, err := pool.Exec(ctx, insert)
		Expect(err).NotTo(HaveOccurred())

		_, err = pool.Exec(ctx, insert)
		Expect(err).To(MatchError(ContainSubstring("one_active_run_per_key")))
	})

	It("shares a database with the deliveries log", func(ctx SpecContext) {
		url := pgtest.Database()
		log, err := deliveries.Connect(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(log.Close)
		s, err := postgres.Connect(ctx, url, "shared")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(s.Close)

		Expect(s.Admit(ctx, "t", "d1", queue("k", "1"))).To(Equal(store.Queued))
		Expect(log.Append(ctx, deliveries.Delivery{Source: "t", DeliveryID: "d1", Kind: "k", Body: []byte("{}")})).To(BeTrue())
	})

	It("migrates once when almanacs start at once", func(ctx SpecContext) {
		url := pgtest.Database()
		errs := make(chan error, 4)
		for range cap(errs) {
			go func() {
				s, err := postgres.Connect(ctx, url, "racing")
				if err == nil {
					s.Close()
				}
				errs <- err
			}()
		}
		for range cap(errs) {
			Expect(<-errs).To(Succeed())
		}
	})
})
