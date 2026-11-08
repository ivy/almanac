package memory_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"

	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/store/memory"
	"github.com/ivy/almanac/store/storetest"
)

var _ = Describe("The memory store", func() {
	storetest.Conformance(func(context.Context) storetest.Open {
		scopes := map[string]*memory.Store{}
		return func(scope string) store.Store {
			if scopes[scope] == nil {
				scopes[scope] = memory.New()
			}
			return scopes[scope]
		}
	})
})
