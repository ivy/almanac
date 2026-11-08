package runtime_test

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanactest"
	"github.com/ivy/almanac/internal/pgtest"
	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/store/memory"
	"github.com/ivy/almanac/store/postgres"
	"github.com/ivy/almanac/store/storetest"
)

var _ = Describe("The runtime on the memory store", func() {
	almanactest.Conformance(func(context.Context) storetest.Open {
		scopes := map[string]*memory.Store{}
		return func(scope string) store.Store {
			if scopes[scope] == nil {
				scopes[scope] = memory.New()
			}
			return scopes[scope]
		}
	})
})

var _ = Describe("The runtime on PostgreSQL", Label("db"), func() {
	almanactest.Conformance(func(ctx context.Context) storetest.Open {
		pool, err := pgxpool.New(ctx, pgtest.Database())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(pool.Close)
		Expect(postgres.Migrate(ctx, pool)).To(Succeed())
		return func(scope string) store.Store { return postgres.New(pool, scope) }
	})
})
