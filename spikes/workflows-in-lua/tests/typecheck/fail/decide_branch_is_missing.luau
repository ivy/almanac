--!strict
local almanac = require("../../../workflows/almanac")

type Cooling = { kind: "Cooling" }
type Passed = { kind: "Passed", repo: string }
type Decide = { Cooling: (Cooling) -> { almanac.Effect }, Passed: (Passed) -> { almanac.Effect } }

-- What `match` gives Rust for free. Here it holds only because `Decide` is written out and named,
-- and nothing makes an author write it.

local decide: Decide = {
	Cooling = function()
		return {}
	end,
}

return decide
