;; Repairs a Renovate pull request whose CI failed, once the update has cooled.
(import-macros {: agent : workflow} :almanac)

;; How old a release must be before anything reads it.
(local min-release-age (* 72 3600))
(local renovate-branch "renovate/")
(local log-limit (* 256 1024))
;; Repairs tried on one pull request before a person is asked.
(local attempt-cap 3)
(local trust-context "almanac/trust")

(agent :fixer {:profile :fixer :prompt "fixer.md" :vars [:repo]})

;; CI failing on a Renovate branch, or finishing on a commit Almanac patched.
(fn needs-attention [event]
  (let [renovate (= renovate-branch (event.branch:sub 1 (length renovate-branch)))]
    (and renovate
         (not event.bookkeeping)
         (or event.failed (and event.patched event.finished)))))

;; What GitHub says now. A payload is only a hint that something changed (R-36).
(fn read-pull [github author event]
  (let [listed (github.pull-for-branch event.repo event.branch)]
    ;; A branch name proves nothing: only Renovate's own pull requests are ours to touch.
    (if (or (= nil listed) (not= listed.author author))
        {:found false}
        (let [pull (github.pull event.repo listed.number)
              checks (github.checks event.repo pull.head)]
          {:found true
           :number listed.number
           :head pull.head
           :conflicted (= pull.mergeable false)
           :failed (= checks.state :red)
           :running (= checks.state :pending)
           :trust-pending checks.trust-pending
           :text pull.text}))))

;; The tail of each failed job's log, where a build's error usually is.
(fn read-logs [github repo sha]
  (accumulate [text (taint.wrap "")
               _ run (ipairs (github.check-runs repo sha))]
    (case (and run.failed (github.job-logs repo run.id))
      log (taint.concat text (taint.tail log log-limit) "\n")
      _ text)))

;; The update has cooled and is failing, so hold it, read its logs, and ask for a repair.
(fn repair [cx github event pull cooled]
  (let [{: repo : branch} event
        head pull.head]
    (cx.hold trust-context repo head)
    (let [logs (cx.observe :logs #(read-logs github repo head))
          patch (cx.repair :fixer {:prompt {: repo}
                                   :input {: logs}
                                   :checkout {: repo :sha head}
                                   : cooled})]
      {:kind :repaired : repo :number pull.number : branch : head : patch})))

(fn gather [cx event]
  (let [github (cx.get :github)
        target (cx.get :scan-target)
        pull (cx.observe :pull #(read-pull github target.author event))
        repo event.repo
        head pull.head]
    (if (or (not pull.found) pull.conflicted pull.running)
        {:kind :nothing}

        (not pull.failed)
        (if pull.trust-pending
            {:kind :passed : repo : head}
            {:kind :nothing})

        (let [attempts (cx.attempts)]
          (if (>= attempts.used attempt-cap)
              {:kind :exhausted : repo : head :asked attempts.asked}
              (let [update (taint.map pull.text :update-of)
                    ages (cx.get :ages)]
                (case (cx.cooled min-release-age (ages.newest update))
                  cooled (repair cx github event pull cooled)
                  _ {:kind :cooling})))))))

(fn stop-and-ask [repo head question body]
  [(effect.attest {:context trust-context :state :failure : repo :sha head})
   (effect.ask {: question :target repo : body})])

;; Where a run ended up, turned into effects.
(fn policy [facts]
  (case facts
    {:kind :nothing} []
    {:kind :exhausted :asked true} []
    {:kind :cooling} [(effect.await "the update's cooldown to pass")]
    {:kind :passed : repo : head}
    [(effect.attest {:context trust-context :state :success : repo :sha head})]
    {:kind :exhausted : repo : head}
    (stop-and-ask repo head "Repairs did not fix this build" (taint.wrap ""))
    {:kind :repaired : repo : number : branch : head : patch}
    (case (patches.check patch head)
      checked [(effect.push {: repo
                             :pull number
                             : branch
                             :message "fix(deps): repair the build after this update"
                             :hold trust-context
                             :patch checked})]
      _ (stop-and-ask repo head "A repair was refused by the patch policy"
                      (taint.map patch :diff)))))

(workflow build-failures
  {:on :BuildChanged
   :key "{repo}:{branch}"
   :intent "Repair a Renovate pull request whose build failed, after its cooldown."
   :may [:Attest :Await :Ask :Push]
   :runs-per-hour 20
   :subjects [(repo repo)]
   :when needs-attention
   :gather gather
   :decide policy})
