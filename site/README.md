# Starlight Starter Kit: Basics

[![Built with Starlight](https://astro.badg.es/v2/built-with-starlight/tiny.svg)](https://starlight.astro.build)

```
npm create astro@latest -- --template starlight
```

> 🧑‍🚀 **Seasoned astronaut?** Delete this file. Have fun!

## 🚀 Project Structure

Inside of your Astro + Starlight project, you'll see the following folders and files:

```
.
├── public/
├── src/
│   ├── assets/
│   ├── content/
│   │   └── docs/
│   └── content.config.ts
├── astro.config.mjs
├── package.json
└── tsconfig.json
```

Starlight looks for `.md` or `.mdx` files in the `src/content/docs/` directory. Each file is exposed as a route based on its file name.

Images can be added to `src/assets/` and embedded in Markdown with a relative link.

Static assets, like favicons, can be placed in the `public/` directory.

## 🧞 Commands

Documentation regressions (from `site/`):

```bash
npm run test:docs
WAZA_BIN=/absolute/path/to/current-main/waza npm run test:examples
npm run build
npx playwright test --project=chromium
```

The example tests extract published YAML and allocate owned fixture storage in
the platform user cache, outside project ancestry. The canonical path and every
ancestor are checked for `skills/`, `.waza.yaml` and `.git`; inherited `TMPDIR`
is never trusted. `WAZA_DOCS_FIXTURE_ROOT` can select another external cache
location, subject to the same fail-closed checks. Child `TMPDIR`, `TMP` and
`TEMP` point into the owned fixture and are removed after the test.
Tests explicitly select mock, check positive/negative controls, and always
override `COPILOT_CLI_PATH` with an absolute verified directory: production SDK
setup must reject it before embedded-runtime selection, even if a future
snippet unexpectedly selects SDK. A regression proves an inherited executable
is not used. Live-test environment flags alone do not provide this guard.
Set `WAZA_BIN` explicitly; missing binaries fail instead of skipping.
Published explanation suites are executed against mock with their task/fixture
snippets and retained unrelated scaffold tasks, not merely preflighted; each
selects one explanation task and has an impossible-predicate negative control.
These walkthroughs require Python for native `code` graders. The adversarial
contract consumes the actual command-generated spec through native
`EvalConfig`/path resolution and skill discovery, with missing/mismatched-source
controls. CLI Go tests additionally capture the factory configuration and
resolved execution requests, substituting mock even for SDK-selected wiring.
No manually supplied skill directory can make that contract pass. No live SDK
session is created; these checks are not proof of model invocation or safety.
The Chromium sweep validates built page routes, exact fragments and local image
assets under `/waza/`, while source checks cover public repository Markdown
targets/anchors and native enum/schema/catalog agreement. External link
availability and historical design documents are outside this local gate.
`npm run build` also runs the source-contract checks through `prebuild`, so a
normal docs build cannot omit the repository link/catalog checks.
The Site Build workflow also runs an **Offline documentation examples** job on
relevant docs/runtime changes: it fetches LFS assets, builds the current CLI
through `make build` (real dashboard assets, no stubs), and invokes
`test:examples` with that binary. The built-in Node test runner needs no separate
site dependency installation for this job.

Use Node.js 22.22.0 or later. Astro requires this minimum; the site CI and GitHub
Pages workflows use Node.js 22.22.0.

All commands are run from the root of the project, from a terminal:

| Command                   | Action                                           |
| :------------------------ | :----------------------------------------------- |
| `npm install`             | Installs dependencies                            |
| `npm run dev`             | Starts local dev server at `localhost:4321`      |
| `npm run build`           | Build your production site to `./dist/`          |
| `npm run preview`         | Preview your build locally, before deploying     |
| `npm run astro ...`       | Run CLI commands like `astro add`, `astro check` |
| `npm run astro -- --help` | Get help using the Astro CLI                     |

Site CI also runs the mobile-menu regression tests in Chromium, with JavaScript
enabled and disabled. To run them locally after building the site:

```sh
npx playwright install chromium
npm run test:e2e -- mobile-menu.spec.ts --project=chromium
```

## 👀 Want to learn more?

Check out [Starlight’s docs](https://starlight.astro.build/), read [the Astro documentation](https://docs.astro.build), or jump into the [Astro Discord server](https://astro.build/chat).
