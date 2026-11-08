package advisory_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/advisory"
)

// order is Compare's answer as one value, so a spec can say "not comparable" in one matcher.
func order(ecosystem, a, b string) any {
	o, ok := advisory.Compare(ecosystem, a, b)
	if !ok {
		return "incomparable"
	}
	return o
}

// Edge cases beyond validate.rs. Each expected answer is what the Rust crate's compare, on
// semver 1.0.28, returns for the same input.
var _ = DescribeTable("Compare agrees with the Rust implementation",
	func(ecosystem, a, b string, want any) {
		Expect(order(ecosystem, a, b)).To(Equal(want))
	},
	Entry(nil, "npm", "1.0.0", "1.0.0+build.5", 0),
	Entry(nil, "npm", "1.0.0-alpha", "1.0.0-alpha.1", -1),
	Entry(nil, "npm", "1.0.0-alpha.1", "1.0.0-alpha.beta", -1),
	Entry(nil, "npm", "1.0.0-alpha.beta", "1.0.0-beta", -1),
	Entry(nil, "npm", "1.0.0-beta.2", "1.0.0-beta.11", -1),
	Entry(nil, "npm", "1.0.0-rc.1", "1.0.0", -1),
	Entry(nil, "npm", "1.0.0-01", "1.0.0", "incomparable"),
	Entry(nil, "npm", "1.0.0+01", "1.0.0", 0),
	Entry(nil, "npm", "01.0.0", "1.0.0", "incomparable"),
	Entry(nil, "npm", "1", "1.0.0", 0),
	Entry(nil, "npm", "v1", "1.0.0", 0),
	Entry(nil, "npm", "1.2-rc.1", "1.2.0", "incomparable"),
	Entry(nil, "npm", "1.2.3.4", "1.2.3", "incomparable"),
	Entry(nil, "npm", "1.2.", "1.2.0", "incomparable"),
	Entry(nil, "npm", "", "1.0.0", "incomparable"),
	Entry(nil, "npm", "v", "1.0.0", "incomparable"),
	Entry(nil, "npm", "vv1.0.0", "1.0.0", "incomparable"),
	Entry(nil, "npm", "V1.0.0", "1.0.0", "incomparable"),
	Entry(nil, "npm", "18446744073709551615.0.0", "1.0.0", 1),
	Entry(nil, "npm", "18446744073709551616.0.0", "1.0.0", "incomparable"),
	Entry(nil, "npm", "1.0.0-99999999999999999999999", "1.0.0-100", 1),
	Entry(nil, "npm", "1.0.0-", "1.0.0", "incomparable"),
	Entry(nil, "npm", "1.0.0+", "1.0.0", "incomparable"),
	Entry(nil, "npm", "1.0.0-a..b", "1.0.0", "incomparable"),
	Entry(nil, "npm", "1.0.0+a+b", "1.0.0", "incomparable"),
	Entry(nil, "npm", " 1.0.0", "1.0.0", "incomparable"),
	Entry(nil, "npm", "1.0.0-x-y", "1.0.0-x", 1),
	Entry(nil, "npm", "1.0.0-0", "1.0.0-a", -1),
	Entry(nil, "npm", "1.0.0-A", "1.0.0-a", -1),
	Entry(nil, "crates.io", "1.0", "1", 0),
	Entry(nil, "RubyGems", "1.0.0.pre", "1.0.0", "incomparable"),
	Entry(nil, "Packagist", "v2.0.0", "2.0.0", 0),
	Entry(nil, "NPM", "1.0.0", "2.0.0", -1),
	Entry(nil, "PyPI", "1.+2", "1.2", 0),
	Entry(nil, "PyPI", "1.-2", "1.2", "incomparable"),
	Entry(nil, "PyPI", "1.++2", "1.2", "incomparable"),
	Entry(nil, "PyPI", "1..2", "1.2", "incomparable"),
	Entry(nil, "PyPI", "", "0", "incomparable"),
	Entry(nil, "PyPI", "01.2", "1.2", 0),
	Entry(nil, "PyPI", "1.2.0.0.1", "1.2", 1),
	Entry(nil, "PyPI", "18446744073709551616", "1", "incomparable"),
	Entry(nil, "PyPI", "v1.2", "1.2", "incomparable"),
	Entry(nil, "PyPI", " 1", "1", "incomparable"),
	Entry(nil, "pypi", "2", "10", -1),
	Entry(nil, "Maven", "1.0", "1.0.0", "incomparable"),
	Entry(nil, "Maven", "", "", 0),
	Entry(nil, "İnvalid", "1", "1", 0),
)
