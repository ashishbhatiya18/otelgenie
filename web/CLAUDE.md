# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

otelgenie walks someone from "I have a collector endpoint and a token" to OpenTelemetry data flowing from their servers and clusters. It is one program, `otelgenie`, that serves a page on the user's own machine. All logic is Go; this directory (`web/`) is the page, a Next.js static export embedded in the program.

The project root is the parent directory (`..`), which holds `go.mod` (module `otelgenie`). This directory is one of four parts:

| Path (from project root) | Role |
|---|---|
| `engine/` | All logic. Pure Go, tested natively. |
| `cmd/otelgenie/` | The program: `serve` (the page, the engine behind `/api/call/`, storage behind `/api/store/`, the template at `/api/template`), `update` (fetches the template with git) and `config`. |
| `web/` | The page (this directory). |
| `template-starter/` | A starter template repo; also the fixture the tests load. |

## Commands

```sh
# from the project root
go test ./...                                   # engine and program
go test ./engine/ -run TestVerifyNamesTheRealCause -v    # single test
scripts/build-cli.sh [VERSION]                  # page + program for every platform -> dist/
go run ./cmd/otelgenie serve --ui web/out       # serve a built page without rebuilding the program
# End-to-end on a systemd container with a real collector; needs podman:
OTELGENIE_E2E_RPM=/path/to/otelcol-contrib_<version>_linux_<arch>.rpm go test ./engine -run LinuxEndToEnd -v

# from web/
npm run build    # next build (-> out/) + scripts/gen-headers.mjs
npx tsc --noEmit # typecheck only; there is no lint script and there are no JS tests
```

The page does nothing on its own: it needs the program. To work on it with `npm run dev`, run `go run ./cmd/otelgenie serve --allow-origin http://localhost:3000` beside it and start the dev server with `NEXT_PUBLIC_OTELGENIE_API=http://127.0.0.1:11111`. A change to `engine/` or `cmd/otelgenie/` needs the program restarted; a change to the page needs `npm run build` (and, for the embedded copy, `scripts/build-cli.sh`).

## Who uses what

Engineering authors the template (a git repository, usually on GitHub Enterprise). The observability team is the user of everything else: they install the `otelgenie` program, run `otelgenie update` and `otelgenie serve`, fill in the page, download the bundle (`README.md`, `otelgenie.sh` and the files), and carry it into the airgapped environment, where `otelgenie.sh` does the work.

Releases: pushing a tag `vX.Y.Z` runs `.github/workflows/release.yml`, which builds every platform, publishes the archives (each holds only the program) with `checksums.txt`, and, when the secret `HOMEBREW_TAP_TOKEN` is set, writes `Formula/otelgenie.rb` in `ashishbhatiya18/homebrew-tap` from `scripts/formula.sh`. The formula's `service` block runs `otelgenie serve --addr 127.0.0.1:11111`; `otelgenie install` only asks Homebrew to start that service. The port is fixed (`defaultAddr`).

`scripts/build-cli.sh [VERSION]` builds the page, copies `web/out` into `cmd/otelgenie/ui` (embedded with `go:embed`) and builds the program for every platform into `dist/`.

The page asks `/api/template` (`lib/cli.ts`) on every load and uses what comes back, so there is no stale template and the one-time setup has no template step. The program's settings and everything the page stores live under the user's config directory (`OTELGENIE_HOME` overrides): `config.json`, `fetched.json`, the fetched `template/`, and `data/`. A template address that is a folder is read live and never fetched. For an `https://` address `update` tries git's own credentials, then a token (environment, or `gh auth token`), then the same repository over SSH.

## Architecture

### Engine / shell split

The engine is pure: no storage, no clock and no network. The page supplies time (`now` in every call) and the program supplies persistence. This purity is what makes a recorded journey replayable, so keep `time.Now()`, randomness and I/O out of `engine/`.

The page never contains domain logic. It calls `call(method, payload)` from `lib/engine.ts`, which POSTs JSON to `/api/call/<method>` and returns the parsed answer. Methods are the `case` arms of `dispatch` in `cmd/otelgenie/dispatch.go` (`describeTemplate`, `fingerprint`, `compare`, `analyzeSpec`, `analyzeChart`, `project`, `create`, `open`, `act`, `archive`, `file`, `export`, `analyze`, `doctor`, `version`). All methods share one flat `request` struct; adding a method means a new `case` plus any new field on that struct. One engine call runs at a time (`engineLock`).

Screens are engine-driven too: `Journey.State()` returns `View`s made of `ViewBlock`s and a `Form` (`engine/views.go`), and `components/Journey.tsx` renders them generically. To change what a step says or asks, edit the engine, not the component.

### Event log and persistence

A journey's state is an append-only, hash-chained event log (`engine/log.go`). Event types starting `in.` are user inputs; `d.` are what the engine derived. `Journey.apply` is the only place state changes, and `Journey.process` derives everything that follows from one input event.

The contract between engine, page and store:

1. The page calls `act` (or `create`); the program keeps the live `Journey` in an in-memory map keyed by id.
2. The engine returns a `Delta` (new events + content-addressed blobs) and the new state.
3. The page commits the delta with `db.appendDelta`, which the program writes to disk in one atomic file replace (`cmd/otelgenie/store.go`), **before** rendering the next screen.
4. On reload, or after the program restarts, `db.loadJourney` + `call('open', …)` rebuilds the journey from stored events. Nothing is recomputed.

Replay (`engine/replay.go`) depends on this: `Rerun` feeds recorded inputs through `process` and compares each derived event with the recording; `Analyze` produces the reviewer report and PDF from an exported `Bundle`. Any change that makes `process` non-deterministic, or changes derived output for old inputs, shows up as replay mismatches.

Step order per target is `discover → confirm → review → apply → verify → done` (`engine/steps.go`). Pasted script output is parsed as `KEY=VALUE` blocks between markers (`engine/parse.go`) and passed through `Redact` before being stored.

What is stored (`lib/db.ts` is a thin client of `/api/store/<op>`): `data/kv.json` (`profile`, `commit`, `open_journey`, `drafts`), `data/snapshots/<commit>.json`, and `data/journeys/<id>.json` (summary, events, blobs). Journeys pin the template snapshot they started with. Nothing is kept in the browser.

### Templates and generated files

A template is a GitHub repo with `otelgenie.json` (collector version, artifacts, limits), `catalog/**.yaml`, and `scripts/vm/*.sh`. Each catalog file is plain collector config that the collector loads unmodified, with `#!` header lines (`otelgenie`, `discover`, `needs`, `docs`) for what collector config cannot express. Values use the collector's `${env:NAME:-default}` syntax.

Generation (`engine/generate.go`) copies catalog files to `conf.d/` byte for byte; all per-server variation lives in the env file. Every pipeline and receiver in a catalog file is suffixed with its component name because the collector's config merge replaces lists. `TestStarterTemplateIsClean` lints `template-starter/`, so catalog edits must keep passing `ParseEntry`.

### Two kinds of target

A target is a server (`vm`) or a cluster (`k8s`); `Target.Kind` picks the scripts (`scripts/<kind>/`), the generator and the screens. Server behaviour must stay byte-identical, because old journeys replay against it, so cluster behaviour is added in branches on the kind rather than by editing the server path. Most of it is in `engine/k8s.go`.

In a cluster the OpenTelemetry Operator runs two collectors, `node` (DaemonSet) and `cluster` (Deployment). Catalog files with `targets=k8s` are collector config tagged `collector=node|cluster`, or carry `role=collector` (the `OpenTelemetryCollector` resource the config is merged into, one pair per kind of cluster) or `role=manifest` (applied as written). `collectorManifest` does the merge with the collector's own semantics: maps combine, lists are replaced. A component with a `role=collector` file is a "platform" (`gke`, `eks`, `ocp`); a cluster target must select exactly one. Receiver and pipeline names only have to be unique among files for the same target kind.

Data flow in a cluster: applications send to the `node` collector, the `node` collectors send to the `cluster` collector (its `otlp/gateway` receiver, set in each platform's `collector-cluster.yaml`), and only the `cluster` collector exports to the destination. Servers outside the cluster send to that same receiver through an Ingress. The Ingress file is marked `when=servers`, so it is only written when the project has a cluster plus something that cannot run in one (`serversBesideCluster`). Its address is only known once it exists, so the process step that creates it carries `Ask: "ingress"`; the form asks there, puts the answer in `Inputs.Ingress` and rebuilds the plan. Until then the server step is `Waiting` and the package has no `server/` folder. `sendsTo` picks the address a target sends to. The receiver checks the same bearer token the destination uses. Everything in a cluster lives in the namespace `observability` (`k8sNamespace`, also written out in the catalog files). On servers the collector runs as the user in `settings.service_user` (`observability` in the starter), created by `scripts/vm/apply.sh`, under the systemd limits from `settings`.

The cluster scripts print the same `KEY=VALUE` blocks and the same keys (`service`, `restarts`, `placeholders`, `file.*`, `m.*`, `err.*`) as the server ones, so `evaluateVerify` is shared.

### The project form and package

`components/NewProject.tsx` is the seven-step form (the `check=true` components are their own step, "Additional capability"): project basics, components, microservices (only with the `app` component; name, language, Swagger and Helm chart each), headers found in each microservice's Swagger/OpenAPI JSON (`engine/spec.go`, `AnalyzeSpec`), the additional checks, the download, and the install guide (the engine's `ProcessStep`s). `engine/project.go` (`BuildProject`) produces the process steps, the README and a tar.gz from the inputs alone, by generating files for draft targets that have no discovery facts. The guided journey later regenerates the same files per target with facts. From the first completed step the form's answers are saved as a `Draft` (`kv` key `drafts`) on every step change, so the project shows in the home list and can be carried on with; the draft is removed when the guided setup turns it into a journey.

`engine/chart.go` (`AnalyzeChart`) reads a microservice's Helm chart as text, since templates are not valid YAML, to find the value that feeds the pod template's annotations; `BuildProject` then writes `services/<name>/values-otel.yaml` carrying the `inject-<language>` annotation, or tells the user which template to edit.

The package also carries `otelgenie.sh` (the template's `scripts/project/otelgenie.sh`, copied as written) with `project.env` and `prerequisites.txt`, which `BuildProject` writes for it. Unlike the per-target scripts it prints PASS/FAIL lines for a person, not `KEY=VALUE` blocks for the page. Template artifacts of kind `addon` (kube-state-metrics, the metrics server, cert-manager) each carry a `check` command; they feed both the install step of the process and that script.

Chosen headers travel in `Inputs.Services` and surface as three catalog inputs: `header_dimensions` (a YAML list the collector parses when expanding the variable), `request_headers` and `response_headers`. `${env:NAME}` inside `role=manifest` and `role=guide` files is filled in by the engine, because nothing else expands it there.

### Enforced guarantees (don't break these)

- **Nothing leaves the machine.** `public/_headers` sets a CSP whose `connect-src` is `'self'` only, and the program serves the page under those headers. The only network use is `otelgenie update`, which runs git.
- **Other sites cannot reach the program.** `guard` in `cmd/otelgenie/main.go` refuses `/api/` requests whose `Origin` is not the program's own address (or the one named with `--allow-origin`) and POSTs that are not JSON. The program listens on `127.0.0.1` unless told otherwise.
- **Inline script hashes.** `scripts/gen-headers.mjs` replaces `__INLINE_SCRIPT_HASHES__` in `out/_headers` with the sha256 of each inline script Next emitted, rather than allowing `'unsafe-inline'`.
- **Static export.** `next.config.mjs` uses `output: 'export'` on the Pages Router: no API routes, server components, middleware or `getServerSideProps`. The page's server is the Go program.
- **Secrets.** The collector access token is typed where the installers run, never in the page. A token used to fetch the template is handed to git in its environment for that one run and written nowhere.

### Not built yet

An end-to-end test for clusters (nothing under `scripts/k8s/` or `catalog/k8s/` has run against a real cluster), moving an in-progress journey to a newer template version, and an offline copy of upstream docs.
