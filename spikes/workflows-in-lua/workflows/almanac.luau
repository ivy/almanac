--!strict
-- The types of what the host provides. `luau-analyze` reads this file. At run time
-- `require("./almanac")` returns the host's own module instead, and this file is never loaded.

export type Slug = string
export type Sha = string

-- Observed values. A script can hold these, pass them on, and apply host functions by name.
export type PullText = { map: (self: PullText, name: "update_of") -> Update }
export type Update = { read __update: "host only" }
export type Text = { narrow: (self: Text, name: "is_blank") -> boolean }
export type Patch = { map: (self: Patch, name: "diff") -> Text }
export type Answer = { read __answer: "host only" }

-- Proofs. A script can write a table of this shape and satisfy the analyzer, and the host refuses it.
export type Cooled = { read __cooled: "host only" }
export type Checked = { read __checked: "host only" }

export type BuildChanged = {
	read repo: Slug,
	read branch: Slug,
	read sha: Sha,
	read failed: boolean,
	read finished: boolean,
	read patched: boolean,
	read bookkeeping: boolean,
}

export type PullView = {
	read found: boolean,
	read number: number,
	read head: Sha,
	read conflicted: boolean,
	read failed: boolean,
	read running: boolean,
	read trust_pending: boolean,
	read text: PullText,
}

export type Attempts = { read used: number, read asked: boolean }

export type FixerInput = {
	logs: Text,
	["$checkout"]: { repo: Slug, sha: Sha },
}

export type WorkerInput = { flag: boolean?, name: Slug? }

export type Cx = {
	read pull: (event: BuildChanged) -> PullView,
	read logs: (repo: Slug, sha: Sha) -> { read text: Text },
	read cooled: (min_age: number, update: Update) -> Cooled?,
	read attempts: () -> Attempts,
	read hold: (context: string, repo: Slug, sha: Sha) -> (),
	read repair: (agent: "fixer", vars: { repo: Slug }, input: FixerInput, cooled: Cooled) -> Patch,
	read read: (agent: "reader", vars: {}, input: {}) -> Answer,
	read act: (agent: "writer", vars: {}, input: WorkerInput) -> { read ok: boolean },
}

export type Effect =
	{ kind: "Attest", context: string, state: "Pending" | "Success" | "Failure", repo: Slug, sha: Sha }
	| { kind: "Await", awaiting: string }
	| { kind: "File", title: string, target: Slug, body: (Text | string)? }
	| { kind: "Ask", question: string, target: Slug, body: (Text | string)? }
	| { kind: "Push", repo: Slug, pull: number, branch: Slug, message: string, hold: string?, patch: Checked }

export type EffectKind = "Attest" | "Await" | "File" | "Ask" | "Push"

export type Agent = { profile: "quarantined" | "fixer" | "worker", prompt: string, vars: { [string]: string }? }

export type Workflow<Event, Facts, Decide> = {
	name: string,
	on: string,
	key: string,
	intent: string,
	may: { EffectKind },
	runs_per_hour: number,
	subjects: { { repo: string } }?,
	when: ((event: Event) -> boolean)?,
	scan: string?,
	reads: { string }?,
	agents: { [string]: Agent }?,
	literals: { string }?,
	gather: (cx: Cx, event: Event) -> Facts,
	decide: Decide,
}

local almanac = {}

function almanac.check_patch(patch: Patch, base: Sha): (Checked?, string?)
	error("provided by the host")
end

return almanac
