# otelgenie

One program that takes someone who knows nothing about OpenTelemetry from
"I have a collector endpoint and a token" to telemetry flowing from their servers and clusters.

`otelgenie serve` opens a page on your own machine. The logic is Go, inside the program; the
page is Next.js (static export) with Tailwind, embedded in it. What you enter is kept in files
on that machine and sent nowhere.

## Who does what

| Who | Does |
|---|---|
| Engineering | Authors the template: a git repository saying what can be collected and how. |
| Observability | Installs the `otelgenie` program, fills in the page, downloads the bundle, and installs it. |

```
otelgenie config template-url https://github.example.com/observability/otel-templates
otelgenie update        # fetch the latest template and check it
otelgenie serve --open  # the page, at http://127.0.0.1:11111/
```

The page produces one bundle: `README.md` (the install guide), `otelgenie.sh` and every file
they refer to. The bundle is carried into the airgapped environment, where `otelgenie.sh`
checks, installs and validates.

- `otelgenie update` uses git, so it signs in to an enterprise GitHub the way git on that
  machine already does (SSH key or credential helper). A template with problems is reported
  and not put in place of the one in use.
- `template-url` may also be a folder; it is then read as it is, every time the page loads.
- Build the program with `scripts/build-cli.sh VERSION`; the results are in `dist/`.

## What the user needs to know

- The collector endpoint (the access token is typed on the server, never in the page)
- Project and environment
- Which components the project has
- Which of metrics, events, logs and traces to enable

Everything else comes from the template, from a read-only script the user runs on
the server and pastes back, or from defaults.

## Layout

| Path | What it is |
|---|---|
| `engine/` | All logic: catalog, fingerprint, event log, steps, file generation, replay, PDF. Pure Go, tested natively. |
| `web/` | The page. `lib/engine.ts` and `lib/db.ts` talk to the program that serves it. |
| `cmd/otelgenie/` | The `otelgenie` program: serves the page, runs the engine for it, stores what it remembers, and fetches the template. |
| `scripts/build-cli.sh` | Builds the program, with the page inside it, for every platform. |
| `template-starter/` | A starter template repository. Copy it into its own GitHub repo. |

## Templates

A template is a GitHub repository with:

- `otelgenie.json`: collector version, artifact list, resource limits.
- `catalog/**.yaml`: one file per component and signal. Each is plain collector
  configuration that the collector loads unmodified, with `#!` comment lines on top
  carrying what collector config cannot express (`otelgenie`, `discover`, `needs`, `docs`).
  `category=` on the `otelgenie` line groups components in the page's search list.
  Values use the collector's own `${env:NAME:-default}` syntax.
- `scripts/vm/*.sh` and `scripts/k8s/*.sh`: static scripts driven by generated data files.

Files are deployed to `conf.d/` byte for byte and merged by the collector itself, so
upstream documentation applies as written. Every pipeline and receiver is suffixed with
its component name because the collector's merge replaces lists.

## Starting a project

The project form has seven steps:

1. **Project**: name, environment (dev, sit, uat, perf, production, or one you name),
   destination URL, and what to enable.
2. **Components**: databases, platforms, kinds of cluster, applications and frontend
   applications.
3. **Microservices** (optional, and only with the `app` component): for each one its name,
   language, Swagger or OpenAPI JSON, and Helm chart (packaged `.tgz` or its folder). The
   chart shows where the instrumentation annotation has to go: when it passes a value such
   as `podAnnotations` to the pod template, the package gets a
   `services/<name>/values-otel.yaml` for `helm upgrade`; otherwise the process names the
   template to edit. Without a chart it gives a `kubectl patch` command.
4. **Metrics from headers**: every header the API descriptions declare, and what each is
   good for. Headers with a small set of values split the request metrics; numeric ones are
   recorded per request; ones that are unique per request or carry credentials are left out.
5. **Additional capability**: optional checks on each server itself (connectivity, files,
   ports, processes).
6. **Download**: `README.md` with the install guide, the `otelgenie.sh` script and every
   file the guide refers to, as one package.
7. **Install Guide**: every stage in order, from moving the package to the jump server and
   checking the images and the destination, through installing and validating the
   OpenTelemetry Operator, to validating that data flows. Where a stage produces something
   later stages need (the Ingress address), the guide asks for it there. The guided,
   one-target-at-a-time setup can be started from here too.

The API descriptions and charts are read on your machine and not stored; only what was found in them is.
The headers reach the collector as the variable `OTELGENIE_HEADER_DIMENSIONS`, which
`header-metrics.yaml` gives to the span-metrics connector, and reach the Java and Python
agents through the `Instrumentation` resource.

A catalog file with `role=guide` is instructions rather than configuration: a Markdown file
that goes into the package under `guides/`. The starter uses it for React, React Native and
Flutter, which send from the user's device and so are set up in their own code.

## Kubernetes clusters

How data flows: applications send to the collector on their node (the DaemonSet), the node
collectors send to the cluster's collector (the Deployment), and only that one sends to the
destination. Servers outside the cluster (Jenkins, databases, nginx) send to the cluster's
collector through an Ingress. Creating it is a step of the process; once it has an address,
the form asks for it there and fills in the server steps that follow. Everything is created
in the namespace `observability`. On servers the collector is the systemd service
`otelgenie-otelcol`, running as the user `observability` with memory and CPU limits.

A project that includes a kind of cluster (`gke`, `eks` or `ocp` in the starter) can add
clusters as well as servers. A cluster is set up through `kubectl` (or `oc`) from any
machine logged in to it, and the OpenTelemetry Operator runs the collectors:

- a DaemonSet, one collector on every node: kubelet metrics, pod logs, and OTLP from applications;
- a Deployment, one collector for the cluster: kube-state-metrics, the metrics server, and events.

Catalog files for clusters are marked `targets=k8s` and live under `catalog/k8s/`:

- Collector configuration, one file per component and signal as for servers, with
  `collector=node` or `collector=cluster` saying which collector it belongs to.
- `role=collector`: the `OpenTelemetryCollector` resource for one of the two collectors.
  Each kind of cluster brings its own pair. The operator takes one configuration per
  collector, so otelgenie fills `spec.config` by merging the base, the resource's own
  `config`, then every selected file for that collector, the way the collector merges
  `conf.d` (maps combine, lists are replaced).
- `role=manifest`: any other resource, applied as written (RBAC, the `Instrumentation`
  resource that auto-instruments applications).

Two extra `discover` forms exist for clusters: `svc=<name> -> VAR as={name}.{ns}.svc:{port}`
finds a Service in any namespace, and `pod=<fieldPath> -> VAR` sets a variable on the
collector pod from the downward API. Values and secrets reach the collectors through the
Secret `otelgenie-env`, which `apply.sh` creates.

The operator's manifest is an `operator` artifact in `otelgenie.json` and is carried in by
hand like a package; on OpenShift it comes from OperatorHub instead. `image` artifacts
list every image the cluster needs. When the one-time setup names an internal registry,
image addresses in the generated resources are rewritten to it and `images.txt` lists
where each image is expected.

## Guarantees and how they are enforced

- **Nothing leaves the machine.** The page is served under a Content-Security-Policy that
  lets it talk only to the program that served it. The program listens on `127.0.0.1` and
  refuses requests that come from any other web site. Its only network use is
  `otelgenie update`, which runs git.
- **Survives restarts.** Every action is written to disk, in one step, before the next
  screen is shown. Stop the program, start it again, and each project is where it was.
- **Reviewable.** A project's recorded session can be exported, checked for integrity and
  replayed step by step.
- **No secrets in the page.** Access tokens are typed where the installers run.

## Develop

```sh
go test ./...                              # engine and program
scripts/build-cli.sh 0.0.0-dev             # the page and the program, into dist/
dist/otelgenie-darwin-arm64 config template-url "$PWD/template-starter"
dist/otelgenie-darwin-arm64 serve --open
```

Needs Go 1.22 or newer and Node. To work on the page with a development server, see
`web/CLAUDE.md`.

## Not built yet

- An end-to-end test for clusters. The cluster scripts and resources are covered by engine
  tests only; they have not been run against a real GKE, EKS or OpenShift cluster.
- Installing kube-state-metrics, the metrics server or cert-manager. They are found if
  present and reported if missing.
- Components other than `app` inside a cluster (postgres, nginx and the rest are for servers).
- Moving a journey in progress to a newer template version. Journeys stay on the template they started with.
- An offline copy of upstream documentation; the catalog carries links only.
