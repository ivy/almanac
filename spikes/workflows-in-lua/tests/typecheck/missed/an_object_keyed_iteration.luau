--!strict
local almanac = require("../../../workflows/almanac")

local cx = {} :: almanac.Cx
local event = {} :: almanac.BuildChanged
local text = {} :: almanac.Text
local patch = {} :: almanac.Patch
local cooled = {} :: almanac.Cooled
local input = {} :: almanac.FixerInput

local keyed: { [{}]: number } = {}
for key, value in keyed do
	local e: almanac.Effect = { kind = "Await", awaiting = tostring(value) }
end
