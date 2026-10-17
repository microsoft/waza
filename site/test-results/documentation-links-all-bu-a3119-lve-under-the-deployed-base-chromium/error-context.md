# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: documentation-links.spec.ts >> all built internal page routes, fragments and image assets resolve under the deployed base
- Location: e2e/documentation-links.spec.ts:14:1

# Error details

```
Error: expect(received).toEqual(expected) // deep equality

- Expected  - 1
+ Received  + 3

- Array []
+ Array [
+   "/waza/concepts/ -> /waza/concepts/guides/dependency-recovery/: HTTP 404",
+ ]
```

# Page snapshot

```yaml
- generic [active] [ref=f28e1]:
  - link "Skip to content" [ref=f28e2] [cursor=pointer]:
    - /url: "#_top"
  - generic [ref=f28e3]:
    - banner [ref=f28e4]:
      - generic [ref=f28e5]:
        - link "waza" [ref=f28e7] [cursor=pointer]:
          - /url: /waza/
        - navigation [ref=f28e9]:
          - link "Getting Started" [ref=f28e10] [cursor=pointer]:
            - /url: /waza/getting-started/
          - link "Guides" [ref=f28e11] [cursor=pointer]:
            - /url: /waza/guides/eval-yaml/
          - link "Reference" [ref=f28e12] [cursor=pointer]:
            - /url: /waza/reference/cli/
          - link "About" [ref=f28e13] [cursor=pointer]:
            - /url: /waza/about/
        - button "Search" [ref=f28e16] [cursor=pointer]:
          - generic [aria-hidden] [ref=f28e19]: Search
          - generic [ref=f28e20]:
            - generic [ref=f28e21]: Ctrl
            - generic [ref=f28e22]: K
        - generic [ref=f28e23]:
          - link "GitHub" [ref=f28e25] [cursor=pointer]:
            - /url: https://github.com/microsoft/waza
          - generic [ref=f28e30]:
            - generic [ref=f28e31]: Select theme
            - combobox "Select theme" [ref=f28e32] [cursor=pointer]:
              - option "Dark"
              - option "Light"
              - option "Auto" [selected]
    - navigation "Main":
      - list [ref=f28e36]:
        - listitem [ref=f28e37]:
          - link "Concepts and Learning Path" [ref=f28e38] [cursor=pointer]:
            - /url: /waza/concepts/
        - listitem [ref=f28e39]:
          - link "Quick Start" [ref=f28e40] [cursor=pointer]:
            - /url: /waza/quick-start/
        - listitem [ref=f28e41]:
          - link "Getting Started" [ref=f28e42] [cursor=pointer]:
            - /url: /waza/getting-started/
        - listitem [ref=f28e43]:
          - group [ref=f28e44]:
            - generic "Guides" [ref=f28e45] [cursor=pointer]
            - list [ref=f28e49]:
              - listitem [ref=f28e50]:
                - link "Writing Eval Specs" [ref=f28e51] [cursor=pointer]:
                  - /url: /waza/guides/eval-yaml/
              - listitem [ref=f28e52]:
                - link "Offline Eval Preflight" [ref=f28e53] [cursor=pointer]:
                  - /url: /waza/guides/preflight/
              - listitem [ref=f28e54]:
                - link "Dependency Recovery" [ref=f28e55] [cursor=pointer]:
                  - /url: /waza/guides/dependency-recovery/
              - listitem [ref=f28e56]:
                - link "Controlled comparisons" [ref=f28e57] [cursor=pointer]:
                  - /url: /waza/guides/controlled-comparisons/
              - listitem [ref=f28e58]:
                - link "Preserving Existing Workflows" [ref=f28e59] [cursor=pointer]:
                  - /url: /waza/guides/compatibility/
              - listitem [ref=f28e60]:
                - link "Evaluating Custom Agents" [ref=f28e61] [cursor=pointer]:
                  - /url: /waza/guides/custom-agents/
              - listitem [ref=f28e62]:
                - link "Validators & Graders" [ref=f28e63] [cursor=pointer]:
                  - /url: /waza/guides/graders/
              - listitem [ref=f28e64]:
                - link "Spec Verification" [ref=f28e65] [cursor=pointer]:
                  - /url: /waza/guides/spec-verify/
              - listitem [ref=f28e66]:
                - link "Token Limits" [ref=f28e67] [cursor=pointer]:
                  - /url: /waza/guides/token-limits/
              - listitem [ref=f28e68]:
                - link "Web Dashboard" [ref=f28e69] [cursor=pointer]:
                  - /url: /waza/guides/dashboard/
              - listitem [ref=f28e70]:
                - link "Dashboard Lab (Prototype)" [ref=f28e71] [cursor=pointer]:
                  - /url: /waza/guides/dashboard-lab/
              - listitem [ref=f28e72]:
                - link "Explore the Dashboard" [ref=f28e73] [cursor=pointer]:
                  - /url: /waza/guides/dashboard-explore/
              - listitem [ref=f28e74]:
                - link "CI/CD Integration" [ref=f28e75] [cursor=pointer]:
                  - /url: /waza/guides/ci-cd/
              - listitem [ref=f28e76]:
                - link "OpenTelemetry Tracing" [ref=f28e77] [cursor=pointer]:
                  - /url: /waza/guides/otel/
              - listitem [ref=f28e78]:
                - link "Adversarial Harness" [ref=f28e79] [cursor=pointer]:
                  - /url: /waza/guides/adversarial/
              - listitem [ref=f28e80]:
                - link "Snapshot & Replay" [ref=f28e81] [cursor=pointer]:
                  - /url: /waza/guides/snapshot-replay/
              - listitem [ref=f28e82]:
                - link "Azure Storage" [ref=f28e83] [cursor=pointer]:
                  - /url: /waza/guides/azure-storage/
        - listitem [ref=f28e84]:
          - group [ref=f28e85]:
            - generic "Reference" [ref=f28e86] [cursor=pointer]
            - list [ref=f28e90]:
              - listitem [ref=f28e91]:
                - link "CLI Commands" [ref=f28e92] [cursor=pointer]:
                  - /url: /waza/reference/cli/
              - listitem [ref=f28e93]:
                - link "Project Configuration" [ref=f28e94] [cursor=pointer]:
                  - /url: /waza/reference/waza-yaml/
              - listitem [ref=f28e95]:
                - link "Statistical Fields" [ref=f28e96] [cursor=pointer]:
                  - /url: /waza/reference/statistical-fields/
              - listitem [ref=f28e97]:
                - link "YAML Schema" [ref=f28e98] [cursor=pointer]:
                  - /url: /waza/reference/schema/
              - listitem [ref=f28e99]:
                - link "Rubric Library" [ref=f28e100] [cursor=pointer]:
                  - /url: /waza/reference/rubrics/
              - listitem [ref=f28e101]:
                - link "Schema Changes" [ref=f28e102] [cursor=pointer]:
                  - /url: /waza/reference/schema-changes/
              - listitem [ref=f28e103]:
                - link "Releases" [ref=f28e104] [cursor=pointer]:
                  - /url: /waza/reference/releases/
        - listitem [ref=f28e105]:
          - link "About" [ref=f28e106] [cursor=pointer]:
            - /url: /waza/about/
    - generic [ref=f28e108]:
      - complementary [ref=f28e109]:
        - navigation [ref=f28e114]:
          - heading "On this page" [level=2] [ref=f28e115]
          - list [ref=f28e116]:
            - listitem [ref=f28e117]:
              - link "Overview" [ref=f28e118] [cursor=pointer]:
                - /url: "#_top"
            - listitem [ref=f28e119]:
              - link "Quick Start" [ref=f28e120] [cursor=pointer]:
                - /url: "#quick-start"
            - listitem [ref=f28e121]:
              - link "Configuration Sections" [ref=f28e122] [cursor=pointer]:
                - /url: "#configuration-sections"
              - list [ref=f28e123]:
                - listitem [ref=f28e124]:
                  - link "paths" [ref=f28e125] [cursor=pointer]:
                    - /url: "#paths"
                - listitem [ref=f28e126]:
                  - link "files" [ref=f28e127] [cursor=pointer]:
                    - /url: "#files"
                - listitem [ref=f28e128]:
                  - link "defaults" [ref=f28e129] [cursor=pointer]:
                    - /url: "#defaults"
                - listitem [ref=f28e130]:
                  - link "cache" [ref=f28e131] [cursor=pointer]:
                    - /url: "#cache"
                - listitem [ref=f28e132]:
                  - link "server" [ref=f28e133] [cursor=pointer]:
                    - /url: "#server"
                - listitem [ref=f28e134]:
                  - link "dev" [ref=f28e135] [cursor=pointer]:
                    - /url: "#dev"
                - listitem [ref=f28e136]:
                  - link "tokens" [ref=f28e137] [cursor=pointer]:
                    - /url: "#tokens"
                - listitem [ref=f28e138]:
                  - link "graders" [ref=f28e139] [cursor=pointer]:
                    - /url: "#graders"
                - listitem [ref=f28e140]:
                  - link "storage" [ref=f28e141] [cursor=pointer]:
                    - /url: "#storage"
            - listitem [ref=f28e142]:
              - link "Complete Example" [ref=f28e143] [cursor=pointer]:
                - /url: "#complete-example"
            - listitem [ref=f28e144]:
              - link "Next Steps" [ref=f28e145] [cursor=pointer]:
                - /url: "#next-steps"
      - main [ref=f28e147]:
        - heading "Project Configuration (.waza.yaml)" [level=1] [ref=f28e150]
        - generic [ref=f28e152]:
          - generic [ref=f28e153]:
            - paragraph [ref=f28e154]:
              - text: The
              - code [ref=f28e155]: .waza.yaml
              - text: file configures project-level defaults for Waza. Place it in your project root — Waza searches up the directory tree (max 10 levels) to find it.
            - generic [ref=f28e156]:
              - heading "Quick Start" [level=2] [ref=f28e157]
              - link "Section titled “Quick Start”" [ref=f28e158] [cursor=pointer]:
                - /url: "#quick-start"
            - figure ".waza.yaml" [ref=f28e164]:
              - code [ref=f28e168]:
                - generic [ref=f28e169]: "paths:"
                - generic [ref=f28e171]: "skills: skills/"
                - generic [ref=f28e173]: "evals: evals/"
                - generic [ref=f28e175]: "results: results/"
                - generic [ref=f28e179]: "files:"
                - generic [ref=f28e181]: "evalFile: eval.yaml"
                - generic [ref=f28e183]: "taskGlob: tasks/*.yaml"
                - generic [ref=f28e185]: "taskFileSuffix: .yaml"
                - generic [ref=f28e189]: "defaults:"
                - generic [ref=f28e191]: "model: claude-sonnet-4.6"
                - generic [ref=f28e193]: "timeout: 300"
                - generic [ref=f28e195]: "workers: 0"
              - button "Copy to clipboard" [ref=f28e198] [cursor=pointer]
            - generic [ref=f28e200]:
              - heading "Configuration Sections" [level=2] [ref=f28e201]
              - link "Section titled “Configuration Sections”" [ref=f28e202] [cursor=pointer]:
                - /url: "#configuration-sections"
            - generic [ref=f28e207]:
              - heading "paths" [level=3] [ref=f28e208]
              - link "Section titled “paths”" [ref=f28e209] [cursor=pointer]:
                - /url: "#paths"
            - paragraph [ref=f28e214]: Directory paths relative to the project root.
            - table [ref=f28e215]:
              - rowgroup [ref=f28e216]:
                - row [ref=f28e217]:
                  - columnheader "Field" [ref=f28e218]
                  - columnheader "Type" [ref=f28e219]
                  - columnheader "Default" [ref=f28e220]
                  - columnheader "Description" [ref=f28e221]
              - rowgroup [ref=f28e222]:
                - row [ref=f28e223]:
                  - cell [ref=f28e224]:
                    - code [ref=f28e225]: skills
                  - cell "string" [ref=f28e226]
                  - cell [ref=f28e227]:
                    - code [ref=f28e228]: skills/
                  - cell "Directory containing skill definitions" [ref=f28e229]
                - row [ref=f28e230]:
                  - cell [ref=f28e231]:
                    - code [ref=f28e232]: evals
                  - cell "string" [ref=f28e233]
                  - cell [ref=f28e234]:
                    - code [ref=f28e235]: evals/
                  - cell "Directory containing evaluation specs" [ref=f28e236]
                - row [ref=f28e237]:
                  - cell [ref=f28e238]:
                    - code [ref=f28e239]: results
                  - cell "string" [ref=f28e240]
                  - cell [ref=f28e241]:
                    - code [ref=f28e242]: results/
                  - cell "Directory for evaluation results" [ref=f28e243]
            - paragraph [ref=f28e244]:
              - text: Workspace detection also recognizes APM-managed skills compiled under
              - code [ref=f28e245]: <paths.skills>/<name>/.apm/skills/<name>/SKILL.md
              - text: . Keep eval suites in the standard
              - code [ref=f28e246]: evals/<name>/
              - text: directory or next to the source skill directory (for example,
              - code [ref=f28e247]: <paths.skills>/<name>/eval.yaml
              - text: ). If a top-level
              - code [ref=f28e248]: <paths.skills>/<name>/SKILL.md
              - text: exists alongside the APM output, the top-level file takes precedence.
            - generic [ref=f28e249]:
              - heading "files" [level=3] [ref=f28e250]
              - link "Section titled “files”" [ref=f28e251] [cursor=pointer]:
                - /url: "#files"
            - paragraph [ref=f28e256]:
              - text: Filename and glob conventions for generated and discovered eval suites. Defaults keep the standard
              - code [ref=f28e257]: eval.yaml
              - text: and
              - code [ref=f28e258]: tasks/*.yaml
              - text: layout, but projects can opt into more Waza-specific names for editor tooling.
            - table [ref=f28e259]:
              - rowgroup [ref=f28e260]:
                - row [ref=f28e261]:
                  - columnheader "Field" [ref=f28e262]
                  - columnheader "Type" [ref=f28e263]
                  - columnheader "Default" [ref=f28e264]
                  - columnheader "Description" [ref=f28e265]
              - rowgroup [ref=f28e266]:
                - row [ref=f28e267]:
                  - cell [ref=f28e268]:
                    - code [ref=f28e269]: evalFile
                  - cell "string" [ref=f28e270]
                  - cell [ref=f28e271]:
                    - code [ref=f28e272]: eval.yaml
                  - cell [ref=f28e273]:
                    - text: Eval filename generated by
                    - code [ref=f28e274]: waza new
                    - text: and preferred by workspace discovery
                - row [ref=f28e275]:
                  - cell [ref=f28e276]:
                    - code [ref=f28e277]: taskGlob
                  - cell "string" [ref=f28e278]
                  - cell [ref=f28e279]:
                    - code [ref=f28e280]: tasks/*.yaml
                  - cell "Glob written into generated eval files" [ref=f28e281]
                - row [ref=f28e282]:
                  - cell [ref=f28e283]:
                    - code [ref=f28e284]: taskFileSuffix
                  - cell "string" [ref=f28e285]
                  - cell [ref=f28e286]:
                    - code [ref=f28e287]: .yaml
                  - cell "Suffix appended to generated task file basenames" [ref=f28e288]
            - figure [ref=f28e290]:
              - code [ref=f28e292]:
                - generic [ref=f28e293]: "files:"
                - generic [ref=f28e295]: "evalFile: waza-eval.yaml"
                - generic [ref=f28e297]: "taskGlob: tasks/*.waza-task.yaml"
                - generic [ref=f28e299]: "taskFileSuffix: .waza-task.yaml"
              - button "Copy to clipboard" [ref=f28e302] [cursor=pointer]
            - generic [ref=f28e304]:
              - heading "defaults" [level=3] [ref=f28e305]
              - link "Section titled “defaults”" [ref=f28e306] [cursor=pointer]:
                - /url: "#defaults"
            - paragraph [ref=f28e311]: Default execution parameters applied to all commands unless overridden by CLI flags.
            - table [ref=f28e312]:
              - rowgroup [ref=f28e313]:
                - row [ref=f28e314]:
                  - columnheader "Field" [ref=f28e315]
                  - columnheader "Type" [ref=f28e316]
                  - columnheader "Default" [ref=f28e317]
                  - columnheader "Description" [ref=f28e318]
              - rowgroup [ref=f28e319]:
                - row [ref=f28e320]:
                  - cell [ref=f28e321]:
                    - code [ref=f28e322]: engine
                  - cell "string" [ref=f28e323]
                  - cell [ref=f28e324]:
                    - code [ref=f28e325]: copilot-sdk
                  - cell "Execution engine" [ref=f28e326]
                - row [ref=f28e327]:
                  - cell [ref=f28e328]:
                    - code [ref=f28e329]: model
                  - cell "string" [ref=f28e330]
                  - cell [ref=f28e331]:
                    - code [ref=f28e332]: claude-sonnet-4.6
                  - cell "Default model for execution" [ref=f28e333]
                - row [ref=f28e334]:
                  - cell [ref=f28e335]:
                    - code [ref=f28e336]: judgeModel
                  - cell "string" [ref=f28e337]
                  - cell [ref=f28e338]
                  - cell "Model for LLM-as-judge graders" [ref=f28e339]
                - row [ref=f28e340]:
                  - cell [ref=f28e341]:
                    - code [ref=f28e342]: timeout
                  - cell "int" [ref=f28e343]
                  - cell [ref=f28e344]:
                    - code [ref=f28e345]: "300"
                  - cell "Task timeout in seconds" [ref=f28e346]
                - row [ref=f28e347]:
                  - cell [ref=f28e348]:
                    - code [ref=f28e349]: parallel
                  - cell "bool" [ref=f28e350]
                  - cell [ref=f28e351]:
                    - code [ref=f28e352]: "false"
                  - cell "Enable parallel execution" [ref=f28e353]
                - row [ref=f28e354]:
                  - cell [ref=f28e355]:
                    - code [ref=f28e356]: workers
                  - cell "int" [ref=f28e357]
                  - cell [ref=f28e358]:
                    - code [ref=f28e359]: "0"
                  - cell "Number of parallel workers; 0 auto-sizes" [ref=f28e360]
                - row [ref=f28e361]:
                  - cell [ref=f28e362]:
                    - code [ref=f28e363]: verbose
                  - cell "bool" [ref=f28e364]
                  - cell [ref=f28e365]:
                    - code [ref=f28e366]: "false"
                  - cell "Enable verbose output" [ref=f28e367]
                - row [ref=f28e368]:
                  - cell [ref=f28e369]:
                    - code [ref=f28e370]: sessionLog
                  - cell "bool" [ref=f28e371]
                  - cell [ref=f28e372]:
                    - code [ref=f28e373]: "false"
                  - cell "Enable session event logging" [ref=f28e374]
            - generic [ref=f28e375]:
              - heading "cache" [level=3] [ref=f28e376]
              - link "Section titled “cache”" [ref=f28e377] [cursor=pointer]:
                - /url: "#cache"
            - paragraph [ref=f28e382]: Result caching configuration.
            - table [ref=f28e383]:
              - rowgroup [ref=f28e384]:
                - row [ref=f28e385]:
                  - columnheader "Field" [ref=f28e386]
                  - columnheader "Type" [ref=f28e387]
                  - columnheader "Default" [ref=f28e388]
                  - columnheader "Description" [ref=f28e389]
              - rowgroup [ref=f28e390]:
                - row [ref=f28e391]:
                  - cell [ref=f28e392]:
                    - code [ref=f28e393]: enabled
                  - cell "bool" [ref=f28e394]
                  - cell [ref=f28e395]:
                    - code [ref=f28e396]: "false"
                  - cell "Enable result caching" [ref=f28e397]
                - row [ref=f28e398]:
                  - cell [ref=f28e399]:
                    - code [ref=f28e400]: dir
                  - cell "string" [ref=f28e401]
                  - cell [ref=f28e402]:
                    - code [ref=f28e403]: .waza-cache
                  - cell "Cache directory" [ref=f28e404]
            - generic [ref=f28e405]:
              - heading "server" [level=3] [ref=f28e406]
              - link "Section titled “server”" [ref=f28e407] [cursor=pointer]:
                - /url: "#server"
            - paragraph [ref=f28e412]:
              - text: Dashboard server configuration for
              - code [ref=f28e413]: waza serve
              - text: .
            - table [ref=f28e414]:
              - rowgroup [ref=f28e415]:
                - row [ref=f28e416]:
                  - columnheader "Field" [ref=f28e417]
                  - columnheader "Type" [ref=f28e418]
                  - columnheader "Default" [ref=f28e419]
                  - columnheader "Description" [ref=f28e420]
              - rowgroup [ref=f28e421]:
                - row [ref=f28e422]:
                  - cell [ref=f28e423]:
                    - code [ref=f28e424]: port
                  - cell "int" [ref=f28e425]
                  - cell [ref=f28e426]:
                    - code [ref=f28e427]: "3000"
                  - cell "Dashboard server port" [ref=f28e428]
                - row [ref=f28e429]:
                  - cell [ref=f28e430]:
                    - code [ref=f28e431]: resultsDir
                  - cell "string" [ref=f28e432]
                  - cell [ref=f28e433]:
                    - code [ref=f28e434]: results/
                  - cell "Directory served by the dashboard" [ref=f28e435]
            - generic [ref=f28e436]:
              - heading "dev" [level=3] [ref=f28e437]
              - link "Section titled “dev”" [ref=f28e438] [cursor=pointer]:
                - /url: "#dev"
            - paragraph [ref=f28e443]:
              - text: Settings for the
              - code [ref=f28e444]: waza dev
              - text: iterative improvement command.
            - table [ref=f28e445]:
              - rowgroup [ref=f28e446]:
                - row [ref=f28e447]:
                  - columnheader "Field" [ref=f28e448]
                  - columnheader "Type" [ref=f28e449]
                  - columnheader "Default" [ref=f28e450]
                  - columnheader "Description" [ref=f28e451]
              - rowgroup [ref=f28e452]:
                - row [ref=f28e453]:
                  - cell [ref=f28e454]:
                    - code [ref=f28e455]: model
                  - cell "string" [ref=f28e456]
                  - cell [ref=f28e457]:
                    - code [ref=f28e458]: claude-sonnet-4-20250514
                  - cell "Model for dev mode" [ref=f28e459]
                - row [ref=f28e460]:
                  - cell [ref=f28e461]:
                    - code [ref=f28e462]: target
                  - cell "string" [ref=f28e463]
                  - cell [ref=f28e464]:
                    - code [ref=f28e465]: medium-high
                  - cell "Target adherence level" [ref=f28e466]
                - row [ref=f28e467]:
                  - cell [ref=f28e468]:
                    - code [ref=f28e469]: maxIterations
                  - cell "int" [ref=f28e470]
                  - cell [ref=f28e471]:
                    - code [ref=f28e472]: "5"
                  - cell "Maximum improvement iterations" [ref=f28e473]
            - generic [ref=f28e474]:
              - heading "tokens" [level=3] [ref=f28e475]
              - link "Section titled “tokens”" [ref=f28e476] [cursor=pointer]:
                - /url: "#tokens"
            - paragraph [ref=f28e481]:
              - text: Token budget configuration for
              - code [ref=f28e482]: waza check
              - text: and
              - code [ref=f28e483]: waza tokens
              - text: commands.
            - table [ref=f28e484]:
              - rowgroup [ref=f28e485]:
                - row [ref=f28e486]:
                  - columnheader "Field" [ref=f28e487]
                  - columnheader "Type" [ref=f28e488]
                  - columnheader "Default" [ref=f28e489]
                  - columnheader "Description" [ref=f28e490]
              - rowgroup [ref=f28e491]:
                - row [ref=f28e492]:
                  - cell [ref=f28e493]:
                    - code [ref=f28e494]: warningThreshold
                  - cell "int" [ref=f28e495]
                  - cell [ref=f28e496]:
                    - code [ref=f28e497]: "500"
                  - cell "Token count warning threshold" [ref=f28e498]
                - row [ref=f28e499]:
                  - cell [ref=f28e500]:
                    - code [ref=f28e501]: fallbackLimit
                  - cell "int" [ref=f28e502]
                  - cell [ref=f28e503]:
                    - code [ref=f28e504]: "1000"
                  - cell "Default limit for unmatched files" [ref=f28e505]
            - generic [ref=f28e506]:
              - heading "tokens.limits" [level=4] [ref=f28e507]
              - link "Section titled “tokens.limits”" [ref=f28e508] [cursor=pointer]:
                - /url: "#tokenslimits"
            - paragraph [ref=f28e513]: Per-file token limits with glob pattern matching.
            - table [ref=f28e514]:
              - rowgroup [ref=f28e515]:
                - row [ref=f28e516]:
                  - columnheader "Field" [ref=f28e517]
                  - columnheader "Type" [ref=f28e518]
                  - columnheader "Description" [ref=f28e519]
              - rowgroup [ref=f28e520]:
                - row [ref=f28e521]:
                  - cell [ref=f28e522]:
                    - code [ref=f28e523]: defaults
                  - cell "map" [ref=f28e524]
                  - cell [ref=f28e525]:
                    - text: Glob patterns → token limits (e.g.,
                    - code [ref=f28e526]: "\"*.md\": 2000"
                    - text: )
                - row [ref=f28e527]:
                  - cell [ref=f28e528]:
                    - code [ref=f28e529]: overrides
                  - cell "map" [ref=f28e530]
                  - cell "Exact file paths → token limits (higher precedence)" [ref=f28e531]
            - figure [ref=f28e533]:
              - code [ref=f28e535]:
                - generic [ref=f28e536]: "tokens:"
                - generic [ref=f28e538]: "warningThreshold: 2500"
                - generic [ref=f28e540]: "fallbackLimit: 2000"
                - generic [ref=f28e542]: "limits:"
                - generic [ref=f28e544]: "defaults:"
                - generic [ref=f28e546]: "\"SKILL.md\": 500"
                - generic [ref=f28e548]: "\"references/**/*.md\": 1000"
                - generic [ref=f28e550]: "\"*.md\": 2000"
                - generic [ref=f28e552]: "overrides:"
                - generic [ref=f28e554]: "\"README.md\": 3000"
              - button "Copy to clipboard" [ref=f28e557] [cursor=pointer]
            - generic [ref=f28e559]:
              - heading "graders" [level=3] [ref=f28e560]
              - link "Section titled “graders”" [ref=f28e561] [cursor=pointer]:
                - /url: "#graders"
            - paragraph [ref=f28e566]: Grader execution settings.
            - table [ref=f28e567]:
              - rowgroup [ref=f28e568]:
                - row [ref=f28e569]:
                  - columnheader "Field" [ref=f28e570]
                  - columnheader "Type" [ref=f28e571]
                  - columnheader "Default" [ref=f28e572]
                  - columnheader "Description" [ref=f28e573]
              - rowgroup [ref=f28e574]:
                - row [ref=f28e575]:
                  - cell [ref=f28e576]:
                    - code [ref=f28e577]: programTimeout
                  - cell "int" [ref=f28e578]
                  - cell [ref=f28e579]:
                    - code [ref=f28e580]: "30"
                  - cell "Timeout for external grader programs (seconds)" [ref=f28e581]
            - generic [ref=f28e582]:
              - heading "storage" [level=3] [ref=f28e583]
              - link "Section titled “storage”" [ref=f28e584] [cursor=pointer]:
                - /url: "#storage"
            - paragraph [ref=f28e589]:
              - text: Remote result storage for team collaboration. See
              - link "Azure Storage Guide" [ref=f28e590] [cursor=pointer]:
                - /url: ../../guides/azure-storage/
              - text: .
            - table [ref=f28e591]:
              - rowgroup [ref=f28e592]:
                - row [ref=f28e593]:
                  - columnheader "Field" [ref=f28e594]
                  - columnheader "Type" [ref=f28e595]
                  - columnheader "Default" [ref=f28e596]
                  - columnheader "Description" [ref=f28e597]
              - rowgroup [ref=f28e598]:
                - row [ref=f28e599]:
                  - cell [ref=f28e600]:
                    - code [ref=f28e601]: provider
                  - cell "string" [ref=f28e602]
                  - cell [ref=f28e603]
                  - cell [ref=f28e604]:
                    - text: Storage provider (
                    - code [ref=f28e605]: azure-blob
                    - text: )
                - row [ref=f28e606]:
                  - cell [ref=f28e607]:
                    - code [ref=f28e608]: accountName
                  - cell "string" [ref=f28e609]
                  - cell [ref=f28e610]
                  - cell "Azure Storage account name" [ref=f28e611]
                - row [ref=f28e612]:
                  - cell [ref=f28e613]:
                    - code [ref=f28e614]: containerName
                  - cell "string" [ref=f28e615]
                  - cell [ref=f28e616]:
                    - code [ref=f28e617]: waza-results
                  - cell "Blob container name" [ref=f28e618]
                - row [ref=f28e619]:
                  - cell [ref=f28e620]:
                    - code [ref=f28e621]: enabled
                  - cell "bool" [ref=f28e622]
                  - cell [ref=f28e623]:
                    - code [ref=f28e624]: "false"
                  - cell "Enable remote uploads" [ref=f28e625]
            - figure [ref=f28e627]:
              - code [ref=f28e629]:
                - generic [ref=f28e630]: "storage:"
                - generic [ref=f28e632]: "provider: azure-blob"
                - generic [ref=f28e634]: "accountName: myteamwaza"
                - generic [ref=f28e636]: "containerName: waza-results"
                - generic [ref=f28e638]: "enabled: true"
              - button "Copy to clipboard" [ref=f28e641] [cursor=pointer]
            - generic [ref=f28e643]:
              - heading "Complete Example" [level=2] [ref=f28e644]
              - link "Section titled “Complete Example”" [ref=f28e645] [cursor=pointer]:
                - /url: "#complete-example"
            - figure [ref=f28e651]:
              - region [ref=f28e652]:
                - code [ref=f28e653]:
                  - generic [ref=f28e654]: "# yaml-language-server: $schema=https://raw.githubusercontent.com/microsoft/waza/main/schemas/config.schema.json"
                  - generic [ref=f28e658]: "paths:"
                  - generic [ref=f28e660]: "skills: skills/"
                  - generic [ref=f28e662]: "evals: evals/"
                  - generic [ref=f28e664]: "results: results/"
                  - generic [ref=f28e668]: "files:"
                  - generic [ref=f28e670]: "evalFile: eval.yaml"
                  - generic [ref=f28e672]: "taskGlob: tasks/*.yaml"
                  - generic [ref=f28e674]: "taskFileSuffix: .yaml"
                  - generic [ref=f28e678]: "defaults:"
                  - generic [ref=f28e680]: "engine: copilot-sdk"
                  - generic [ref=f28e682]: "model: claude-sonnet-4.6"
                  - generic [ref=f28e684]: "timeout: 300"
                  - generic [ref=f28e686]: "parallel: false"
                  - generic [ref=f28e688]: "workers: 0"
                  - generic [ref=f28e692]: "cache:"
                  - generic [ref=f28e694]: "enabled: false"
                  - generic [ref=f28e696]: "dir: .waza-cache"
                  - generic [ref=f28e700]: "server:"
                  - generic [ref=f28e702]: "port: 3000"
                  - generic [ref=f28e704]: "resultsDir: results/"
                  - generic [ref=f28e708]: "dev:"
                  - generic [ref=f28e710]: "model: claude-sonnet-4-20250514"
                  - generic [ref=f28e712]: "target: medium-high"
                  - generic [ref=f28e714]: "maxIterations: 5"
                  - generic [ref=f28e718]: "tokens:"
                  - generic [ref=f28e720]: "warningThreshold: 500"
                  - generic [ref=f28e722]: "fallbackLimit: 1000"
                  - generic [ref=f28e724]: "limits:"
                  - generic [ref=f28e726]: "defaults:"
                  - generic [ref=f28e728]: "\"*.md\": 800"
                  - generic [ref=f28e730]: "overrides:"
                  - generic [ref=f28e732]: "\"special.md\": 5000"
                  - generic [ref=f28e736]: "graders:"
                  - generic [ref=f28e738]: "programTimeout: 30"
                  - generic [ref=f28e742]: "storage:"
                  - generic [ref=f28e744]: "provider: azure-blob"
                  - generic [ref=f28e746]: "accountName: myteamwaza"
                  - generic [ref=f28e748]: "containerName: waza-results"
                  - generic [ref=f28e750]: "enabled: true"
              - button "Copy to clipboard" [ref=f28e753] [cursor=pointer]
            - generic [ref=f28e755]:
              - heading "Next Steps" [level=2] [ref=f28e756]
              - link "Section titled “Next Steps”" [ref=f28e757] [cursor=pointer]:
                - /url: "#next-steps"
            - list [ref=f28e762]:
              - listitem [ref=f28e763]:
                - strong [ref=f28e764]:
                  - link "CLI Commands" [ref=f28e765] [cursor=pointer]:
                    - /url: ../cli/
                - text: — Full command reference
              - listitem [ref=f28e766]:
                - strong [ref=f28e767]:
                  - link "Token Limits Guide" [ref=f28e768] [cursor=pointer]:
                    - /url: ../../guides/token-limits/
                - text: — Token budget details
              - listitem [ref=f28e769]:
                - strong [ref=f28e770]:
                  - link "Azure Storage" [ref=f28e771] [cursor=pointer]:
                    - /url: ../../guides/azure-storage/
                - text: — Cloud storage setup
          - generic [ref=f28e773]:
            - link [ref=f28e774] [cursor=pointer]:
              - /url: /waza/reference/cli/
              - generic [ref=f28e777]: PreviousCLI Commands
            - link [ref=f28e778] [cursor=pointer]:
              - /url: /waza/reference/statistical-fields/
              - generic [ref=f28e781]: NextStatistical Fields
```

# Test source

```ts
  1  | import { expect, test } from '@playwright/test';
  2  | import { readdirSync, readFileSync } from 'node:fs';
  3  | import { join, relative } from 'node:path';
  4  | import { fileURLToPath } from 'node:url';
  5  | 
  6  | const dist = fileURLToPath(new URL('../dist/', import.meta.url));
  7  | function htmlFiles(directory: string): string[] {
  8  |   return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
  9  |     const path = join(directory, entry.name);
  10 |     return entry.isDirectory() ? htmlFiles(path) : entry.name === 'index.html' ? [path] : [];
  11 |   });
  12 | }
  13 | 
  14 | test('all built internal page routes, fragments and image assets resolve under the deployed base', async ({ page, request }) => {
  15 |   test.setTimeout(120_000);
  16 |   const links = new Map<string, string>();
  17 |   const images = new Set<string>();
  18 |   const failures: string[] = [];
  19 |   for (const file of htmlFiles(dist)) {
  20 |     const route = `/waza/${relative(dist, file).replace(/index\.html$/, '')}`;
  21 |     const response = await page.goto(route);
  22 |     expect(response?.status(), route).toBe(200);
  23 |     const targets = await page.locator('html').evaluate((document) => ({
  24 |       links: [...document.querySelectorAll<HTMLAnchorElement>('a[href]')].map((a) => a.href),
  25 |       images: [...document.querySelectorAll<HTMLImageElement>('img[src]')].map((img) => img.src),
  26 |     }));
  27 |     for (const target of targets.links) {
  28 |       const url = new URL(target);
  29 |       if (url.origin !== new URL(page.url()).origin) continue;
  30 |       if (!/^\/waza(?:\/|$)/.test(url.pathname)) failures.push(`${route}: missing base in ${target}`);
  31 |       links.set(url.pathname + url.hash, route);
  32 |     }
  33 |     for (const target of targets.images) {
  34 |       const url = new URL(target);
  35 |       if (url.origin === new URL(page.url()).origin) images.add(url.pathname);
  36 |     }
  37 |   }
  38 |   for (const [target, source] of links) {
  39 |     const url = new URL(target, 'http://localhost:4321');
  40 |     const response = await request.get(url.pathname);
  41 |     if (response.status() !== 200) failures.push(`${source} -> ${target}: HTTP ${response.status()}`);
  42 |     if (url.hash) {
  43 |       const id = decodeURIComponent(url.hash.slice(1));
  44 |       if (!(await response.text()).includes(`id="${id}"`)) failures.push(`${source} -> ${target}: missing fragment`);
  45 |     }
  46 |   }
  47 |   for (const image of images) expect((await request.get(image)).status(), image).toBe(200);
  48 |   console.log(`Checked ${htmlFiles(dist).length} built pages, ${links.size} distinct internal route/fragments and ${images.size} local images.`);
> 49 |   expect(failures).toEqual([]);
     |                    ^ Error: expect(received).toEqual(expected) // deep equality
  50 | });
  51 | 
  52 | test('beginner learning order and source availability are visible', async ({ page }) => {
  53 |   await page.goto('/waza/quick-start/');
  54 |   const navigation = page.getByRole('navigation', { name: 'Main' });
  55 |   for (const name of ['Concepts and Learning Path', 'Project Configuration', 'Statistical Fields']) {
  56 |     await expect(navigation.getByRole('link', { name, exact: true })).toBeVisible();
  57 |   }
  58 |   await expect(page.locator('main')).toContainText('v0.38.10');
  59 |   await expect(page.locator('main')).toContainText('expect exit 1');
  60 |   await page.goto('/waza/getting-started/');
  61 |   const text = await page.locator('main').innerText();
  62 |   const realRun = text.indexOf('waza run evals/inventory/eval.yaml');
  63 |   for (const prerequisite of ['Authenticate before real execution', 'Before running', 'restore']) {
  64 |     expect(text.indexOf(prerequisite)).toBeGreaterThanOrEqual(0);
  65 |     expect(text.indexOf(prerequisite)).toBeLessThan(realRun);
  66 |   }
  67 |   await expect(page.locator('main')).toContainText('if [ ! -f evals/inventory/eval.yaml ]; then');
  68 |   await expect(page.locator('main')).toContainText('not a provider spending cap');
  69 |   expect(readFileSync(join(dist, 'concepts/index.html'), 'utf8')).toContain('A graded result is not release authority');
  70 | });
  71 | 
```