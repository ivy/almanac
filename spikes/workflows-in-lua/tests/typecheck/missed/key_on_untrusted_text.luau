--!strict
local almanac = require("../../../workflows/almanac")

local cx = {} :: almanac.Cx
local event = {} :: almanac.BuildChanged
local text = {} :: almanac.Text
local patch = {} :: almanac.Patch
local cooled = {} :: almanac.Cooled
local input = {} :: almanac.FixerInput

local definition: almanac.Workflow<almanac.BuildChanged, { kind: string }, {}> = {
	name = "x",
	on = "BuildChanged",
	key = "{title}",
	intent = "x",
	may = { "Await" },
	runs_per_hour = 1,
	gather = function(cx: almanac.Cx, event: almanac.BuildChanged)
		return { kind = "Done" }
	end,
	decide = {},
}
