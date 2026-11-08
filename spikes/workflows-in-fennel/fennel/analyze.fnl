;; Reads a workflow's source as data and says what it declares and what it reaches. Nothing in the
;; source runs here: the reader builds a tree, and this walks it. Macros are refused in workflows,
;; so what the reader sees is what compiles.
(local fennel (require :fennel))

;; Forms that run code at compile time, inline raw Lua, or reach other files. Any of them would make
;; the tree something other than the program.
(local refused {:eval-compiler true
                :include true
                :lua true
                :macro true
                :macros true
                :require true
                :require-macros true})

;; Top-level forms a workflow is made of. Nothing else may run when the chunk loads.
(local declarations {:agent true :fn true :import-macros true :local true :narrower true :workflow true})
(local options {:decide true
                :gather true
                :intent true
                :key true
                :may true
                :on true
                :runs-per-hour true
                :subjects true
                :when true})
(local required [:decide :gather :intent :key :may :on :runs-per-hour])
(local cx-steps {:act true
                 :attempts true
                 :cooled true
                 :get true
                 :hold true
                 :observe true
                 :read true
                 :repair true})
(local agent-verbs {:act true :read true :repair true})
;; What the host's own tables hold, so a misspelt name or a `merge` fails here, not mid-run.
(local closed {:effect {:ask true :attest true :await true :file true :push true}
               :patches {:check true}
               :taint {:concat true :map true :narrow true :tail true :wrap true}})

(fn name-of [node] (when (fennel.sym? node) (tostring node)))

(fn head-of [node] (when (fennel.list? node) (name-of (. node 1))))

;; The part of `a.b.c` before the first dot.
(fn base-of [name] (or (name:match "^([^.:]+)[.:]") name))

(fn line-of [node] (or (and (= :table (type node)) node.line) 0))

;; Calls `visit` on every node under `node`, and on `node`.
(fn walk [node visit]
  (visit node)
  (when (and (= :table (type node)) (not (fennel.sym? node)))
    (if (or (fennel.list? node) (fennel.sequence? node))
        (each [_ child (ipairs node)] (walk child visit))
        (each [key value (pairs node)]
          (walk key visit)
          (walk value visit)))))

(fn analyze [source filename pure-globals]
  (let [errors []
        defs {}
        agents {}
        narrowers {}
        forms []
        narrow-calls []]
    (var workflow nil)

    (fn fail [node code what why next]
      (table.insert errors {: code : what : why : next :line (line-of node)}))

    ;; A value the reader alone can know. `nil` plus a recorded error if it is anything else.
    (fn literal [node context]
      (if (or (= :string (type node)) (= :number (type node)) (= :boolean (type node)))
          node

          (fennel.sequence? node)
          (icollect [_ item (ipairs node)] (literal item context))

          (and (= :table (type node)) (not (fennel.sym? node)) (not (fennel.list? node)))
          (collect [key value (pairs node)] (values key (literal value context)))

          (fail node :load/not-literal
                (.. context " must be written out literally")
                "the manifest is read from the source without running it"
                "write the value in place, not as a name or a call")))

    (fn read-workflow [form]
      (let [spec (. form 3)
            declared {:name (name-of (. form 2))}]
        (each [key value (pairs (or spec {}))]
          (if (not (. options key))
              (fail form :load/unknown-option
                    (.. "unknown workflow option `" key "`")
                    "an option nothing reads would look like it did something"
                    "remove it, or use one of the documented options")

              (or (= key :gather) (= key :decide) (= key :when))
              (tset declared key (name-of value))

              (= key :subjects)
              (tset declared key
                    (icollect [_ call (ipairs value)]
                      (icollect [_ part (ipairs call)] (name-of part))))

              (tset declared key (literal value (.. "`" key "`")))))
        (each [_ key (ipairs required)]
          (when (= nil (. declared key))
            (fail form :load/missing-option
                  (.. "the workflow needs `:" key "`")
                  "a workflow is incomplete without it"
                  (.. "add `:" key "` to its options"))))
        declared))

    (fn compile-narrower [form]
      (let [[_ name params & body] form
            definition (fennel.list (fennel.sym :fn) params (table.unpack body))
            (ok code) (pcall fennel.compile definition
                            {:allowedGlobals pure-globals
                             :filename filename
                             :useMetadata false})]
        (if ok
            (tset narrowers (name-of name) code)
            (fail form :load/narrower-captures
                  (.. "narrower `" (tostring name) "` does not compile alone: " (tostring code))
                  "a narrower reads hostile text, so it sees nothing but its argument"
                  "use only its parameter and the pure globals"))))

    (each [ok ast (fennel.parser source filename)]
      (when ok (table.insert forms ast)))

    (each [_ form (ipairs forms)]
      (walk form (fn [node]
                   (let [head (head-of node)]
                     (when (and head (. refused head))
                       (fail node :load/refused-form
                             (.. "`" head "` is not allowed in a workflow")
                             "it would make the program differ from the source the manifest is read from"
                             "write it as an ordinary function, or add it to the host"))
                     (when head
                       (let [(table-name member) (head:match "^([^.:]+)%.(.+)$")
                             members (and table-name (. closed table-name))]
                         (when (and members (not (. members member)))
                           (fail node :load/not-in-host
                                 (.. "`" table-name "` has no `" member "`")
                                 "what a script can do is a closed set, kept by the host"
                                 "use one of the host's functions"))))
                     (when (= head :taint.narrow)
                       (table.insert narrow-calls node)))))
      (let [head (head-of form)]
        (if (not (. declarations head))
            (fail form :load/not-a-declaration
                  (.. "the top level may only declare, and this is `" (tostring head) "`")
                  "nothing may run when a workflow loads"
                  "move it into a function")

            (or (= head :fn) (= head :local))
            (tset defs (name-of (. form 2)) form)

            (= head :agent)
            (tset agents (. form 2) (literal (. form 3) "an agent"))

            (= head :narrower)
            (compile-narrower form)

            (= head :import-macros)
            (when (not= :almanac (. form 3))
              (fail form :load/refused-form
                    "only `:almanac` macros may be imported"
                    "other macros would make the program differ from its source"
                    "use the macros the host provides"))

            (= head :workflow)
            (set workflow (read-workflow form)))))

    ;; Every form reachable from a function, following calls to the file's own functions. This is
    ;; why a step taken in a helper still reaches the manifest.
    (fn reach [root]
      (let [seen {}
            found []]
        (fn visit [name]
          (let [form (. defs name)]
            (when (and form (not (. seen name)))
              (tset seen name true)
              (table.insert found form)
              (walk form (fn [node]
                           (let [symbol (name-of node)]
                             (when symbol (visit (base-of symbol)))))))))
        (visit root)
        found))

    (fn each-node [root visit]
      (each [_ form (ipairs (reach root))]
        (walk form visit)))

    (each [_ node (ipairs narrow-calls)]
      (let [wanted (. node 3)]
        (when (or (not= :string (type wanted)) (not (. narrowers wanted)))
          (fail node :load/unknown-narrower
                "`taint.narrow` names no declared narrower"
                "only a narrower declared at the top level may read hostile text"
                "declare it with `(narrower name [text] ...)` and name it here"))))

    (when workflow
      (let [needs []
            reads []
            used []]
        (fn add [items item]
          (var present false)
          (each [_ existing (ipairs items)]
            (when (= existing item) (set present true)))
          (when (not present) (table.insert items item)))

        (each-node workflow.gather
          (fn [node]
            (let [head (head-of node)
                  step (and head (head:match "^cx%.(.+)$"))
                  first (and step (. node 2))]
              (when (and head (= :effect (base-of head)))
                (fail node :load/effect-in-gather
                      (.. "`" head "` is called from gather")
                      "only policy produces effects, so that they can be re-run on replay"
                      "return a fact from gather, and turn it into an effect in policy"))
              (when step
                (when (not (. cx-steps step))
                  (fail node :load/unknown-step
                        (.. "`cx` has no step `" step "`")
                        "only the host's steps are recorded"
                        "use one of the steps the host provides"))
                (when (and (or (= step :get) (= step :observe) (. agent-verbs step))
                           (not= :string (type first)))
                  (fail node :load/not-literal
                        (.. "`cx." step "` must name its target literally")
                        "the manifest and the replay log are keyed by this name"
                        "write the name as a string"))
                (when (= :string (type first))
                  (when (= step :get) (add needs first))
                  (when (= step :observe) (add reads first))
                  (when (. agent-verbs step)
                    (let [opts (. node 3)
                          literal? (and (= :table (type opts)) (not (fennel.sym? opts)))
                          prompt (and literal? (. opts :prompt))]
                      (table.insert used
                                    {:agent first
                                     :verb step
                                     :line (line-of node)
                                     :cooled (and literal? (not= nil (. opts :cooled)))
                                     :prompt-keys (when (and (= :table (type prompt))
                                                             (not (fennel.sym? prompt)))
                                                    (icollect [key (pairs prompt)] key))}))))))))

        ;; Purity is checked for everything that runs in the pure VM.
        (each [_ root (ipairs [workflow.decide workflow.when])]
          (when root
            (each-node root
              (fn [node]
                (let [symbol (name-of node)]
                  (when (and symbol (= :cx (base-of symbol)))
                    (fail node :load/impure-policy
                          (.. "`" root "` reaches `" symbol "`")
                          "policy re-runs on replay, so it may read only what it is given"
                          "gather what it needs and pass it in the facts")))))))

        ;; A function's arity is its only signature.
        (each [key count (pairs {:decide 1 :gather 2 :when 1})]
          (let [name (. workflow key)
                form (and name (. defs name))]
            (when (and name (not form))
              (fail (or form 0) :load/unknown-function
                    (.. "`:" key "` names `" name "`, which this file does not define")
                    "the workflow's functions are read from the file"
                    "define it with `fn` at the top level"))
            (when form
              (let [params (. form 3)]
                (when (not= count (length params))
                  (fail form :load/wrong-arity
                        (.. "`" name "` takes " (length params) " arguments, and `:" key
                            "` takes " count)
                        "a policy that took more could be handed something impure"
                        "match the signature for this option"))))))

        (set workflow.needs needs)
        (set workflow.reads reads)
        (set workflow.used used)))

    {: agents : errors : narrowers : workflow}))

{: analyze}
