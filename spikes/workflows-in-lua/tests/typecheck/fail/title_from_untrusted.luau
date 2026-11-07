--!strict
local almanac = require("../../../workflows/almanac")

local cx = {} :: almanac.Cx
local event = {} :: almanac.BuildChanged
local text = {} :: almanac.Text
local patch = {} :: almanac.Patch
local cooled = {} :: almanac.Cooled
local input = {} :: almanac.FixerInput

local e: almanac.Effect = { kind = "Ask", question = text, target = "a/b" }
