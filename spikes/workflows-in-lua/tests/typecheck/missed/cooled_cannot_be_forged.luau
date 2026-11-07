--!strict
local almanac = require("../../../workflows/almanac")

local cx = {} :: almanac.Cx
local event = {} :: almanac.BuildChanged
local text = {} :: almanac.Text
local patch = {} :: almanac.Patch
local cooled = {} :: almanac.Cooled
local input = {} :: almanac.FixerInput

local forged: almanac.Cooled = { __cooled = "host only" }
local fixed = cx.repair("fixer", { repo = "a/b" }, input, forged)
