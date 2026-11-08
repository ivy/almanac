;; The forms a workflow is written in. They carry no checks: `analyze.fnl` reads these same forms
;; as data, and the host decides whether they are allowed, so skipping a macro skips nothing.

;; Declares a workflow. The options are read as data, so only the three functions reach the chunk.
(fn workflow [_name options]
  `{:when ,(. options :when) :gather ,(. options :gather) :decide ,(. options :decide)})

;; Declares an agent. It is read as data and compiles to nothing.
(fn agent [])

;; Declares a function over hostile text. It is compiled on its own by `analyze.fnl`, and runs in a
;; VM of its own, so here it compiles to nothing.
(fn narrower [])

{: agent : narrower : workflow}
