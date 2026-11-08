# Requirements coverage

Where each requirement in [prd.md](prd.md) is proved. `make check` runs every Rust test named here, and `make go-check` every Go spec.

| Requirement | Proved by |
|---|---|
| R-1 Triggers on red CI | `almanacs/renovate/tests/build_failures.rs`: `a_failed_status_works_like_a_failed_check_suite`, `a_red_pull_request_past_its_cooldown_is_patched_and_goes_pending_then_success` |
| R-2 No agent before cooldown | `a_broken_update_inside_its_cooldown_reaches_no_agent_and_awaits`, `an_update_with_no_publish_time_awaits` |
| R-3 Cooldown reconsidered | `a_cooldown_await_is_reconsidered_by_a_scan_once_it_passes` |
| R-4 Fixer gets tainted input, does not push | `the_logs_reach_the_agent_as_tainted_input_and_the_checkout_by_reference`, `crates/claude/tests/real_cli.rs` |
| R-5 Policy before push | `a_rejected_patch_asks_a_person_and_pushes_nothing`, `a_repair_that_takes_back_the_update_is_never_pushed` |
| R-6 Attempt cap | `a_repair_that_keeps_failing_stops_at_the_cap_and_asks_once`, `crates/definitions/tests/chains.rs` |
| R-7 Status held during a repair | `a_red_pull_request_past_its_cooldown_is_patched_and_goes_pending_then_success` |
| R-8 Security trigger | `almanacs/renovate/tests/security_fixes.rs`: `the_label_also_triggers_it`, `an_ordinary_update_is_ignored` |
| R-9 Pending first | `pending_is_posted_before_any_other_work` |
| R-10 Advisory validation | `crates/advisory/tests/validate.rs` |
| R-11 Young fix asks | `a_brand_new_fix_asks_a_person_and_stays_pending` |
| R-12 Reviewer can only veto | `a_reviewers_concerns_veto_passing_checks`, `a_model_can_never_approve_what_a_check_refused` |
| R-13 Verdict statuses | `a_valid_fix_goes_pending_then_success`, `a_target_that_is_not_the_patched_version_fails_and_asks` |
| R-14 Red security update repaired | By routing: `github.check_suite.completed` goes to `build_failures` for every Renovate branch |
| R-15, R-16 Authoring | `crates/fails/tests/ui/`, `crates/definitions/tests/workflows.rs` |
| R-17 Prompts checked | `crates/fails/tests/ui/fail/prompt_*` |
| R-18 Agents discovered | `crates/fails/tests/ui/pass/ok_workflow_macro.rs`, `the_manifest_shows_each_agents_capabilities` |
| R-19 Profiles | `crates/fails/tests/ui/fail/fixer_*`, `the_manifest_shows_a_fixers_grant` |
| R-20 Patch policy | `crates/almanac/tests/patch.rs` |
| R-55 Titles | `crates/fails/tests/ui/fail/title_*`, `file_title_is_a_string`, `crates/almanac/tests/taint.rs` |
| R-21 Push only to its own branch | `crates/github/tests/client.rs`: `pushing_to_another_branch_is_refused`, `pushing_to_a_fork_is_refused`; `effect_has_no_merge` |
| R-22 Cooled proof | `crates/fails/tests/ui/fail/cooled_*`, `fixer_needs_cooled`, `crates/almanac/tests/cooled.rs` |
| R-23 Sandbox enforces capability | `crates/sandbox/tests/podman.rs`, `crates/claude/tests/real_cli.rs` |
| R-52 Remote-shaped `Sandbox` | `FakeSandbox` holds nothing on the host; [ADR 1](adrs/0001-agent-sandbox.md) maps every operation |
| R-24 Dedupe, seen only once persisted | `crates/almanac/tests/store_postgres.rs`: `a_delivery_that_failed_to_persist_is_accepted_when_redelivered` |
| R-25 One run per key | `testing::conformance`, `the_database_itself_refuses_a_second_active_run_for_a_key` |
| R-26 Chains contained | `crates/definitions/tests/chains.rs` |
| R-27 Budgets | `testing::conformance`, `a_tripped_budget_asks_once_and_held_work_runs_after_an_hour` |
| R-28 Concurrency and lanes | `a_backfill_does_not_starve_a_live_event`, `independent_keys_run_concurrently_but_one_key_never_does` |
| R-29 Broker | `crates/broker/tests/receive.rs` |
| R-53 Durable cursors | `crates/definitions/tests/broker_flow.rs` |
| R-54 Daemon | `crates/definitions/tests/daemon.rs`, `almanacs/renovate/tests/daemon.rs`, `tools/verify-units.sh` |
| R-30 to R-33 Scan, backfill, simulate, origin | `crates/definitions/tests/origins.rs` |
| R-34 Live and dry-run | `a_dry_run_records_what_would_apply_and_calls_no_provider`, `a_dry_run_leaves_a_live_run_everything_still_to_do`, `a_read_only_pass_writes_nothing_and_calls_no_model` |
| R-35 Catch-up | `a_lost_webhook_is_found_by_the_next_scheduled_scan`, `a_sweep_redelivers_what_failed_and_only_once` |
| R-36 `gather` reads current state | `all_three_origins_produce_identical_decisions_for_the_same_state` |
| R-37, R-38 Projections | `xtask/tests/generate.rs`, `a_new_enum_value_from_github_does_not_break_ingest` |
| R-39 Hand-written sources | `crates/fails/tests/ui/fail/payload_field_rejects_string.rs`, `crates/advisory/src/cve_now.rs` |
| R-40 Providers checked at startup | `startup_fails_with_a_diagnostic_when_a_provider_is_missing` |
| R-41 Tracker | `crates/github/tests/tracker.rs` |
| R-42 Diagnostics | `crates/almanac/tests/diagnostics.rs`, `crates/almanac/src/diag.rs`; Go: `diag/diag_test.go`, `config/config_test.go` "Config diagnostics" |
| R-43 Manifest | `the_manifest_names_the_prompt_file_and_its_variables`, `a_new_almanac_builds_tests_and_describes_itself` |
| R-44 Config only tightens | `crates/almanac/tests/limits.rs`, `a_secret_in_config_points_at_its_line_and_says_where_it_belongs`; Go: `config/limits_test.go`, `config/config_test.go` "a secret in config points at its line and says where it belongs", "can never widen what the binary declares" |
| R-45 PostgreSQL state | `crates/almanac/tests/store_postgres.rs`, `workflows_on_postgres.rs` |
| R-46 Scaffolding | `crates/cli/tests/scaffold.rs` |
| R-47 Fleet discovery | `discovery_skips_archived_repositories_and_other_owners`, `an_app_finds_every_repository_past_the_first_page` |
| R-48, R-49 One state per pull request | `almanacs/renovate/tests/fleet.rs` |
| R-50 Conflicts | `a_conflict_waits_for_renovate_unless_the_branch_carries_a_patch` |
| R-51 No CI | `a_repository_with_no_ci_is_reported_as_having_no_checks` |

## Not met

- **R-14** has no single end-to-end test of a security pull request going red, being repaired, and passing. Each half is tested alone.
- **R-50** covers the report only. A conflict on a patched branch files no question, so a person sees it only by reading the report.
- **R-41** files issues through GitHub. A real delivery of one has not been exercised, because no App exists yet.
- **Real webhook shapes** are spec-checked, not captured, for the same reason.
- **Repair quality** is barely measured. One real session's output was kept, and its repair was correct. Five others were lost to a harness fault, since fixed, and were not rerun, so the rate on type and lint failures is unknown.
- **pnpm in a sandbox** arrives through the fetch step, which downloads the latest version each time, about 64 MB. A repository with no root `pnpm-lock.yaml` gets none.
- **The token budget** counts cached tokens, and its default has not been tried against a long real session. `almanacs/renovate` also stops a session at $6.
