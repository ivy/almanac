--!strict
local almanac = require("../../../workflows/almanac")

-- `--!strict` does not require annotations. Left off, `facts` accepts any field, and this reads one
-- no outcome has. The host's strict tables catch it, on this line, when it runs.
local decide = {
	Passed = function(facts): { almanac.Effect }
		return { { kind = "Await", awaiting = facts.nonexistent } }
	end,
}

return decide
