--!strict
-- Repairs a Renovate pull request whose CI failed, once the update has cooled.
local almanac = require("./almanac")

type Cx = almanac.Cx
type BuildChanged = almanac.BuildChanged
type Effect = almanac.Effect
type Patch = almanac.Patch
type Sha = almanac.Sha
type Slug = almanac.Slug
type Text = almanac.Text

local MIN_RELEASE_AGE = 72 * 3600 -- How old a release must be before anything reads it.
local ATTEMPT_CAP = 3 -- Repairs tried on one pull request before a person is asked.
local RENOVATE_BRANCH = "renovate/"
local TRUST = "almanac/trust"
local COOLING = "the update's cooldown to pass"
local COMMIT = "fix(deps): repair the build after this update"

-- Where a run ended up. `decide` turns this into effects.
type Nothing = { kind: "Nothing" } -- The pull request is gone, conflicted, or not failing.
type Cooling = { kind: "Cooling" }
type Passed = { kind: "Passed", repo: Slug, head: Sha } -- CI passed on a patched commit.
type Exhausted = { kind: "Exhausted", repo: Slug, head: Sha, asked: boolean } -- The cap is reached.
type Repaired = { kind: "Repaired", repo: Slug, number: number, branch: Slug, head: Sha, patch: Patch }
type Facts = Nothing | Cooling | Passed | Exhausted | Repaired

-- One branch per outcome. Naming this type is what makes a missing branch an analyzer error.
type Decide = {
	Nothing: (Nothing) -> { Effect },
	Cooling: (Cooling) -> { Effect },
	Passed: (Passed) -> { Effect },
	Exhausted: (Exhausted) -> { Effect },
	Repaired: (Repaired) -> { Effect },
}

-- CI failing on a Renovate branch, or finishing on a commit Almanac patched.
local function needs_attention(event: BuildChanged): boolean
	local renovate = event.branch:sub(1, #RENOVATE_BRANCH) == RENOVATE_BRANCH
	return renovate and not event.bookkeeping and (event.failed or (event.patched and event.finished))
end

local function gather(cx: Cx, event: BuildChanged): Facts
	local pull = cx.pull(event)
	if not pull.found or pull.conflicted or pull.running then
		return { kind = "Nothing" }
	end
	local repo, head = event.repo, pull.head
	if not pull.failed then
		if pull.trust_pending then
			return { kind = "Passed", repo = repo, head = head }
		end
		return { kind = "Nothing" }
	end
	local attempts = cx.attempts()
	if attempts.used >= ATTEMPT_CAP then
		return { kind = "Exhausted", repo = repo, head = head, asked = attempts.asked }
	end
	local update = pull.text:map("update_of")
	local cooled = cx.cooled(MIN_RELEASE_AGE, update)
	if not cooled then
		return { kind = "Cooling" }
	end
	cx.hold(TRUST, repo, head)
	local logs = cx.logs(repo, head)
	local input = { logs = logs.text, ["$checkout"] = { repo = repo, sha = head } }
	local patch = cx.repair("fixer", { repo = repo }, input, cooled)
	return { kind = "Repaired", repo = repo, number = pull.number, branch = event.branch, head = head, patch = patch }
end

local function stop_and_ask(repo: Slug, head: Sha, question: string, body: (Text | string)?): { Effect }
	return {
		{ kind = "Attest", context = TRUST, state = "Failure", repo = repo, sha = head },
		{ kind = "Ask", question = question, target = repo, body = body },
	}
end

local decide: Decide = {
	Nothing = function()
		return {}
	end,
	Cooling = function()
		return { { kind = "Await", awaiting = COOLING } }
	end,
	Passed = function(facts: Passed)
		return { { kind = "Attest", context = TRUST, state = "Success", repo = facts.repo, sha = facts.head } }
	end,
	Exhausted = function(facts: Exhausted)
		if facts.asked then
			return {}
		end
		return stop_and_ask(facts.repo, facts.head, "Repairs did not fix this build")
	end,
	Repaired = function(facts: Repaired)
		local checked = almanac.check_patch(facts.patch, facts.head)
		if not checked then
			local question = "A repair was refused by the patch policy"
			return stop_and_ask(facts.repo, facts.head, question, facts.patch:map("diff"))
		end
		return {
			{
				kind = "Push",
				repo = facts.repo,
				pull = facts.number,
				branch = facts.branch,
				message = COMMIT,
				hold = TRUST,
				patch = checked,
			},
		}
	end,
}

local workflow: almanac.Workflow<BuildChanged, Facts, Decide> = {
	name = "build-failures",
	on = "BuildChanged",
	key = "{repo}:{branch}",
	intent = "Repair a Renovate pull request whose build failed, after its cooldown.",
	may = { "Attest", "Await", "Ask", "Push" },
	runs_per_hour = 20,
	subjects = { { repo = "repo" } },
	scan = "build_failures",
	when = needs_attention,
	reads = { "pull", "logs", "cooled" },
	agents = {
		fixer = { profile = "fixer", prompt = "fixer.md", vars = { repo = "Slug" } },
	},
	literals = { TRUST, COOLING, COMMIT },
	gather = gather,
	decide = decide,
}

return workflow
