# Report states

`almanac-renovate report` gives every open Renovate pull request exactly one state, with a reason.

| State | Meaning | Who acts |
|---|---|---|
| `mergeable` | No conflict, checks pass, and any `almanac/trust` status is `success`. | Renovate, if the repository automerges. Otherwise a note says so. |
| `in-progress` | Almanac is working on it. | Almanac |
| `waiting` | Something named in the reason has to happen first. | Whoever the reason names |
| `needs-person` | Almanac has stopped and a person must decide. | You |
| `blocked-by-config` | A setting prevents the right outcome. The reason names it. | You, by changing that setting |

## Reasons

| State | Reason | What it means |
|---|---|---|
| `in-progress` | a repair is due | CI failed and the update has cooled, so a repair will run. |
| `in-progress` | Almanac is validating it | `almanac/trust` is `pending` while a run finishes. |
| `waiting` | CI to finish | Checks are still running. |
| `waiting` | the update's cooldown to pass | The release is too new for an agent to read. |
| `waiting` | Renovate to rebase it | It has a merge conflict that Renovate can resolve. |
| `waiting` | GitHub to compute mergeability | GitHub has not worked this out yet. |
| `needs-person` | a conflict on a branch carrying an Almanac patch | Renovate would drop the patch, so a person decides. |
| `needs-person` | repairs did not fix the build | Almanac reached its attempt cap of three. |
| `needs-person` | it failed trust checks | `almanac/trust` is `failure`. See the question on the pull request. |
| `needs-person` | nothing says when this update was published | Its cooldown can never pass, so no agent will read it. |
| `needs-person` | nothing tests this repository | It is held, and no check can attest it. Add a workflow. |
| `blocked-by-config` | set platformAutomerge to false, or require the almanac/trust check | GitHub's auto-merge would ignore `almanac/trust`. |
| `blocked-by-config` | add Almanac's author to gitIgnoredAuthors, or Renovate will not merge a patched branch | Renovate would treat a patched branch as edited. |

## Notes

A `mergeable` pull request can carry a note:

- **no checks ran on this pull request.** It counts as mergeable, and Renovate will not automerge it, because nothing tested it.
- **automerge is off, so this will not merge itself.** It is ready for you to merge.

Almanac reads a repository's Renovate file and any `github>owner/repo` preset it extends. Settings in the runner's global configuration are not visible to it.

A person is asked on the pull request itself, with a mention and an assignment. Almanac does not read replies. To move on, fix what it names, or merge or close the pull request.
