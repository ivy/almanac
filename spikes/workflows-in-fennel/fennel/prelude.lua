-- Runs first in every VM, with the real globals, and returns what a script may see. A script gets
-- `env` as its only globals, so nothing here is reachable except what `env` and `cx` name.
local yield = coroutine.yield
local error, next, select, setmetatable = error, next, select, setmetatable
local tostring_raw, type = tostring, type
local format, find = string.format, string.find
local sort, unpack = table.sort, table.unpack

-- `tostring` of a table or userdata prints its address, which differs between runs.
local function safe_tostring(value)
  local kind = type(value)
  if kind == "string" or kind == "number" or kind == "boolean" or kind == "nil" then
    return tostring_raw(value)
  end
  return "<" .. kind .. ">"
end

local function before(a, b)
  local kind_a, kind_b = type(a), type(b)
  if kind_a ~= kind_b then
    return kind_a < kind_b
  end
  if kind_a == "number" or kind_a == "string" then
    return a < b
  end
  error("a table key must be a number or a string", 0)
end

-- `next` walks a table in hash order, and hashes are seeded per VM, so the same table can iterate
-- differently in a replay. This walks in sorted order instead.
local function sorted_pairs(items)
  local keys, count = {}, 0
  for key in next, items do
    count = count + 1
    keys[count] = key
  end
  sort(keys, before)
  local at = 0
  return function()
    at = at + 1
    local key = keys[at]
    if key ~= nil then
      return key, items[key]
    end
  end
end

-- These tables are shared with the string metatable, so they are changed in place.
string.dump = nil
string.format = function(spec, ...)
  if find(spec, "%%[-+ #0]*%d*%.?%d*p") then
    error("`%p` prints an address, which differs between runs", 2)
  end
  local args, count = { ... }, select("#", ...)
  for at = 1, count do
    local kind = type(args[at])
    if kind ~= "string" and kind ~= "number" and kind ~= "boolean" and kind ~= "nil" then
      args[at] = safe_tostring(args[at])
    end
  end
  return format(spec, unpack(args, 1, count))
end
math.random = nil
math.randomseed = nil

local env = {
  assert = assert,
  error = error,
  ipairs = ipairs,
  math = math,
  pairs = sorted_pairs,
  select = select,
  string = string,
  table = table,
  tonumber = tonumber,
  tostring = safe_tostring,
  type = type,
  utf8 = utf8,
}

-- Fennel's `case` expands to `_G.type`, so `_G` must exist. It names this table, not the real globals.
env._G = env

-- A table whose missing fields are mistakes, not `nil`.
local function strict(items, what)
  return setmetatable(items, {
    __index = function(_, key)
      error(what .. " has no field `" .. safe_tostring(key) .. "`", 2)
    end,
    __newindex = function(_, key)
      error(what .. " cannot be changed: `" .. safe_tostring(key) .. "`", 2)
    end,
  })
end

local function reader(provider, operations)
  local reads = {}
  for _, operation in next, operations do
    reads[operation] = function(...)
      return yield(provider, operation, ...)
    end
  end
  return strict(reads, provider)
end

-- `ages` is filled in by the host: its only operation builds a read for `cx.cooled` to perform.
local providers = {
  github = reader("github", { "pull-for-branch", "pull", "checks", "check-runs", "job-logs" }),
  ages = {},
}

local cx = strict({
  get = function(name)
    local value = yield("get", name)
    return providers[name] or value
  end,
  observe = function(name, read)
    return yield("observe", name, read)
  end,
  attempts = function()
    return yield("attempts")
  end,
  cooled = function(min_age, read)
    return yield("cooled", min_age, read)
  end,
  hold = function(context, repo, sha)
    return yield("hold", context, repo, sha)
  end,
  repair = function(agent, options)
    return yield("repair", agent, options)
  end,
  read = function(agent, options)
    return yield("read", agent, options)
  end,
  act = function(agent, options)
    return yield("act", agent, options)
  end,
}, "cx")

return { env = env, cx = cx, providers = providers, strict = strict }
