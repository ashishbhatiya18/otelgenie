import { useEffect, useRef, useState } from 'react';
import { db, Draft, JourneyMeta } from '../lib/db';
import { call, download, fileToBase64, nowISO } from '../lib/engine';
import { ComponentPicker } from './ComponentPicker';
import { Profile } from './Setup';
import { Button, Choice, Command, ErrorNote, Field, inputClass, Summary, Wizard } from './ui';

const SIGNALS: [string, string][] = [
  ['metrics', 'Metrics'],
  ['events', 'Events'],
  ['logs', 'Logs'],
  ['traces', 'Traces'],
];

export const STEPS = ['Project', 'Components', 'Microservices', 'Metrics from headers', 'Additional capability', 'Download', 'Install Guide'];

// The download and the install guide are built from every answer before them.
const PLAN_FROM = STEPS.indexOf('Download');

const ENVS = ['dev', 'sit', 'uat', 'perf', 'production'];
const OTHER_ENV = 'others';

type Header = { name: string; in: string; kind: string; metric: string; operations: string[] };
type Chart = { name: string; version: string; workloads: { kind: string; file: string; key?: string }[]; injected: boolean };
type Service = {
  name: string;
  language?: string;
  file: string;
  report: { title: string; version: string; operations: number; headers: Header[] } | null;
  error: string;
  // Headers the user switched off, as "in name".
  off: string[];
  chart?: Chart | null;
  chartError?: string;
};

const LANGUAGES: [string, string][] = [
  ['java', 'Java'],
  ['python', 'Python'],
  ['go', 'Go'],
];

// Lets a file input take a whole folder; React's types do not know the attribute.
const FOLDER = { webkitdirectory: '' } as object;
const CHART_TEXT = /\.(ya?ml|tpl|txt)$/;
const CHART_FILE_LIMIT = 1 << 20;

function chartNote(c: Chart): string {
  const head = `${c.name} ${c.version}`.trim();
  if (c.workloads.length === 0) return `${head}: no Deployment, StatefulSet or DaemonSet template was found in it.`;
  const w = c.workloads[0];
  const where = w.key
    ? `${w.kind} in ${w.file}; the annotation goes in through the value ${w.key}.`
    : `${w.kind} in ${w.file}; no value reaches the pods' annotations, so the template needs an edit.`;
  return `${head}: ${where}${c.injected ? ' It already carries an inject annotation.' : ''}`;
}

const emptyService: Service = { name: '', language: 'java', file: '', report: null, error: '', off: [], chart: null, chartError: '' };
const keyOf = (h: Header) => `${h.in} ${h.name}`;
const usable = (h: Header) => h.kind === 'breakdown' || h.kind === 'value';
const toggle = (list: string[], v: string) => (list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);

const KIND_LABEL: Record<string, string> = {
  breakdown: 'Splits request metrics',
  value: 'Recorded per request',
  unique: 'Not used',
  secret: 'Never captured',
};

// The questions for a new project, a few at a time: what it is, its
// microservices, its components, what the API descriptions make possible,
// the whole process, and the package to take away.
export function NewProject({
  profile,
  snapshot,
  info,
  draft,
  onOpen,
  onCancel,
}: {
  profile: Profile;
  snapshot: any;
  info: any;
  // A project saved part-way through, to carry on with.
  draft?: Draft;
  onOpen: (id: string) => void;
  onCancel?: () => void;
}) {
  const saved = draft?.answers;
  const draftId = useRef(draft?.id ?? crypto.randomUUID());
  const created = useRef(draft?.created ?? nowISO());
  const [step, setStep] = useState(draft?.step ?? 0);
  const [form, setForm] = useState({ project: '', envChoice: '', envOther: '', endpoint: '', ingress: '', target: '', ...saved?.form });
  const [signals, setSignals] = useState<string[]>(saved?.signals ?? ['metrics', 'logs', 'traces']);
  const [components, setComponents] = useState<string[]>(saved?.components ?? []);
  const [services, setServices] = useState<Service[]>(saved?.services ?? []);
  // The languages the project's applications are written in; asked once the app component is chosen.
  const [stacks, setStacks] = useState<string[]>(saved?.stacks ?? ['java']);
  const [clusterFirst, setClusterFirst] = useState<boolean>(saved?.clusterFirst ?? true);
  const [plan, setPlan] = useState<any>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const env = form.envChoice === OTHER_ENV ? form.envOther.trim() : form.envChoice;
  const choosable = (info?.components ?? []).filter((c: any) => !c.always);
  const software = choosable.filter((c: any) => !c.check);
  const checks = choosable.filter((c: any) => c.check);
  const picked = (list: any[]) => list.filter((c) => components.includes(c.name)).map((c) => c.name);
  // A kind of cluster among the components means there is a cluster to set up.
  const hasCluster = software.some((c: any) => c.platform && components.includes(c.name));
  const kind = hasCluster && clusterFirst ? 'k8s' : 'vm';
  // Microservices are instrumented through the app component, so they only count when it is chosen.
  const appChosen = components.includes('app');
  const named = appChosen ? services.filter((s) => s.name.trim()) : [];
  const offered = LANGUAGES.filter(([id]) => stacks.includes(id));
  // A microservice whose language is no longer among the stacks takes the first one that is.
  const languageOf = (s: Service) => (s.language && stacks.includes(s.language) ? s.language : (offered[0]?.[0] ?? 'java'));

  const setService = (i: number, patch: Partial<Service>) => setServices((all) => all.map((s, n) => (n === i ? { ...s, ...patch } : s)));

  const inputs = () => ({
    project: form.project,
    env,
    endpoint: form.endpoint,
    // Only a project with a cluster has an Ingress for servers to send through.
    ingress: hasCluster ? form.ingress : '',
    signals,
    components,
    stacks: appChosen ? stacks : [],
    services: named.map((s) => ({
      name: s.name.trim(),
      language: languageOf(s),
      chart: s.chart ? { name: s.chart.name, workloads: s.chart.workloads } : undefined,
      headers: (s.report?.headers ?? []).filter((h) => usable(h) && !s.off.includes(keyOf(h))).map((h) => ({ name: h.name, in: h.in, kind: h.kind })),
    })),
  });

  async function readSpec(i: number, file: File | undefined) {
    if (!file) return setService(i, { file: '', report: null, error: '' });
    try {
      setService(i, { file: file.name, report: await call('analyzeSpec', { text: await file.text() }), error: '', off: [] });
    } catch (err: any) {
      setService(i, { file: file.name, report: null, error: err.message ?? String(err) });
    }
  }

  // A chart comes as one packaged file or as the files of its folder.
  async function readChart(i: number, list: FileList | null) {
    const picked = Array.from(list ?? []);
    if (picked.length === 0) return setService(i, { chart: null, chartError: '' });
    try {
      let chart: Chart;
      if (picked.length === 1 && !picked[0].webkitRelativePath) {
        chart = await call('analyzeChart', { bundle: await fileToBase64(picked[0]) });
      } else {
        const files: Record<string, string> = {};
        for (const f of picked) {
          if (CHART_TEXT.test(f.name) && f.size <= CHART_FILE_LIMIT) files[f.webkitRelativePath || f.name] = await f.text();
        }
        chart = await call('analyzeChart', { files });
      }
      setService(i, { chart, chartError: '' });
    } catch (err: any) {
      setService(i, { chart: null, chartError: err.message ?? String(err) });
    }
  }

  // The process and the package are built from the answers so far, so they
  // are rebuilt whenever the user moves on from the steps that hold them.
  async function goTo(next: number) {
    setError('');
    if (step === 1 && next > step && appChosen && stacks.length === 0) {
      setError('Choose at least one stack for the applications.');
      return;
    }
    setBusy(true);
    try {
      if (next >= PLAN_FROM && next >= step) setPlan(await call('project', { profile, snapshot, inputs: inputs(), now: nowISO() }));
      // From the first step on, the project is in the list and can be picked up later.
      await db.saveDraft({
        id: draftId.current,
        project: form.project.trim(),
        env,
        step: next,
        created: created.current,
        updated: nowISO(),
        answers: { form, signals, components, services, stacks, clusterFirst },
      });
    } catch (err: any) {
      setError(err.message ?? String(err));
      return;
    } finally {
      setBusy(false);
    }
    setStep(next);
  }

  // A project picked up at the process or the download needs its plan built again.
  useEffect(() => {
    if (step < PLAN_FROM || plan) return;
    call('project', { profile, snapshot, inputs: inputs(), now: nowISO() })
      .then(setPlan)
      .catch((err: any) => {
        setError(err.message ?? String(err));
        setStep(PLAN_FROM - 1);
      });
    // Only on opening: after that, goTo builds the plan.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function start() {
    setBusy(true);
    setError('');
    try {
      const id = crypto.randomUUID();
      const now = nowISO();
      const made = await call('create', { id, profile, snapshot, inputs: inputs(), now });
      const meta: JourneyMeta = { id, project: form.project.trim(), env, commit: snapshot.commit, created: now, updated: now, summary: 'Just started' };
      await db.appendDelta(meta, made.delta);
      const added = await call('act', { id, now: nowISO(), action: { type: 'add_target', data: { name: form.target, kind } } });
      await db.appendDelta(meta, added.delta);
      await db.deleteDraft(draftId.current);
      onOpen(id);
    } catch (err: any) {
      setError(err.message ?? String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Wizard steps={STEPS} step={step} onStep={goTo} onFinish={start} finish={busy ? 'Starting…' : 'Start the guided setup'} busy={busy} onCancel={onCancel}>
      {step === 0 && (
        <>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Name of the project">
              <input required className={inputClass} value={form.project} onChange={(e) => setForm({ ...form, project: e.target.value })} />
            </Field>
            <Field label="Environment">
              <select required className={inputClass} value={form.envChoice} onChange={(e) => setForm({ ...form, envChoice: e.target.value })}>
                <option value="" disabled>
                  Choose one
                </option>
                {ENVS.map((e) => (
                  <option key={e} value={e}>
                    {e}
                  </option>
                ))}
                <option value={OTHER_ENV}>others - please specify</option>
              </select>
            </Field>
            {form.envChoice === OTHER_ENV && (
              <Field label="Name of the environment">
                <input required pattern=".*\S.*" className={inputClass} value={form.envOther} onChange={(e) => setForm({ ...form, envOther: e.target.value })} />
              </Field>
            )}
            <Field label="Destination URL" hint="The collector endpoint you were given. The access token is never typed here; you enter it where the installer runs.">
              <input
                required
                className={inputClass}
                placeholder="https://collector.example.com:4318"
                value={form.endpoint}
                onChange={(e) => setForm({ ...form, endpoint: e.target.value })}
              />
            </Field>
          </div>

          <fieldset>
            <legend className="mb-2 text-sm font-medium">What to enable</legend>
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
              {SIGNALS.map(([id, label]) => (
                <Choice key={id} title={label} checked={signals.includes(id)} onChange={() => setSignals(toggle(signals, id))} />
              ))}
            </div>
          </fieldset>
        </>
      )}

      {step === 1 && (
        <>
          <ComponentPicker all={software} selected={components} onChange={setComponents} />
          {appChosen && (
            <fieldset>
              <legend className="text-sm font-medium text-slate-700">What are the applications written in?</legend>
              <p className="mb-2 text-xs text-slate-500">
                Choose every stack in this project. Each one has its own agent, and only the agents chosen here are asked for in your registry.
              </p>
              <div className="grid gap-2 sm:grid-cols-3">
                {LANGUAGES.map(([id, label]) => (
                  <Choice
                    key={id}
                    title={label}
                    hint={id === 'go' ? 'Instrumented from outside the process; needs a privileged container.' : 'Agent injected by the operator.'}
                    checked={stacks.includes(id)}
                    onChange={() => setStacks(toggle(stacks, id))}
                  />
                ))}
              </div>
            </fieldset>
          )}
        </>
      )}

      {step === 2 && (
        <fieldset disabled={!appChosen} className="space-y-3">
          {appChosen ? (
            <p className="text-sm text-slate-600">
              Optional: you can leave this empty and come back to it from the project list. For each microservice, its Swagger or OpenAPI JSON shows which
              headers can become metrics, and its Helm chart shows where the instrumentation annotation has to go. The files are read in this browser and
              are not stored; only what was found in them is.
            </p>
          ) : (
            <p className="rounded-xl border border-dashed border-slate-300 p-4 text-sm text-slate-500">
              Microservices are instrumented through the <span className="font-medium">app</span> component. Add it on the Components step to fill this
              in; otherwise continue.
            </p>
          )}
          {appChosen &&
            services.map((s, i) => (
              <div key={i} className="rounded-xl border border-slate-200 p-3">
                <div className="grid gap-3 sm:grid-cols-2">
                  <Field label="Name">
                    <input required className={inputClass} placeholder="orders-api" value={s.name} onChange={(e) => setService(i, { name: e.target.value })} />
                  </Field>
                  <Field label="Language" hint="Decides which agent the operator injects.">
                    <select className={inputClass} value={languageOf(s)} onChange={(e) => setService(i, { language: e.target.value })}>
                      {offered.map(([id, label]) => (
                        <option key={id} value={id}>
                          {label}
                        </option>
                      ))}
                    </select>
                  </Field>
                  <div>
                    <Field label="Swagger or OpenAPI JSON">
                      <input type="file" accept=".json,application/json" className="block w-full text-sm" onChange={(e) => readSpec(i, e.target.files?.[0])} />
                    </Field>
                    {s.report && (
                      <p className="mt-1 text-xs text-slate-500">
                        {s.report.title || s.file} {s.report.version}: {s.report.operations} operations, {s.report.headers.length} headers found.
                      </p>
                    )}
                    {s.error && <p className="mt-1 text-xs text-red-700">{s.error}</p>}
                  </div>
                  <div>
                    <span className="mb-1 block text-sm font-medium">Helm chart</span>
                    <div className="space-y-1 text-sm">
                      <label className="flex flex-wrap items-center gap-2">
                        <span className="text-xs text-slate-500">Packaged (.tgz)</span>
                        <input type="file" accept=".tgz,.gz,application/gzip" className="block text-sm" onChange={(e) => readChart(i, e.target.files)} />
                      </label>
                      <label className="flex flex-wrap items-center gap-2">
                        <span className="text-xs text-slate-500">or its folder</span>
                        <input type="file" multiple {...FOLDER} className="block text-sm" onChange={(e) => readChart(i, e.target.files)} />
                      </label>
                    </div>
                    {s.chart && <p className="mt-1 text-xs text-slate-500">{chartNote(s.chart)}</p>}
                    {s.chartError && <p className="mt-1 text-xs text-red-700">{s.chartError}</p>}
                  </div>
                </div>
                <div className="mt-2 text-right">
                  <button
                    type="button"
                    className="rounded px-2 py-1 text-xs text-slate-500 hover:bg-slate-100 hover:text-red-700"
                    onClick={() => setServices((all) => all.filter((_, n) => n !== i))}
                  >
                    Remove {s.name.trim() || `microservice ${i + 1}`}
                  </button>
                </div>
              </div>
            ))}
          {appChosen && (
            <Button kind="secondary" onClick={() => setServices((all) => [...all, { ...emptyService }])}>
              Add a microservice
            </Button>
          )}
        </fieldset>
      )}

      {step === 3 && (
        <>
          <p className="text-sm text-slate-600">
            Every microservice gets request count, error count and duration for each operation. A header that takes a small set of values can split those
            numbers, for example by tenant or by channel. Untick any you do not want.
          </p>
          {named.length === 0 && <p className="text-sm text-slate-500">No microservices were named, so there is nothing to read headers from. You can go back and add them, or continue.</p>}
          {named.map((s) => {
            const i = services.indexOf(s);
            return (
              <section key={i} className="rounded-xl border border-slate-200">
                <h4 className="border-b border-slate-200 px-3 py-2 text-sm font-semibold">
                  {s.name.trim()}
                  {s.report && <span className="ml-2 font-normal text-slate-500">{s.report.title} {s.report.version}</span>}
                </h4>
                {!s.report || s.report.headers.length === 0 ? (
                  <p className="p-3 text-sm text-slate-500">
                    {s.report ? 'Its description declares no headers.' : 'No API description was given.'} It still gets request count, errors and duration.
                  </p>
                ) : (
                  <div className="overflow-x-auto">
                    <table className="w-full text-left text-sm">
                      <thead className="text-xs text-slate-500">
                        <tr>
                          <th className="px-3 py-2 font-medium">Use</th>
                          <th className="px-3 py-2 font-medium">Header</th>
                          <th className="px-3 py-2 font-medium">On</th>
                          <th className="px-3 py-2 font-medium">Declared by</th>
                          <th className="px-3 py-2 font-medium">What it gives</th>
                        </tr>
                      </thead>
                      <tbody>
                        {s.report.headers.map((h) => (
                          <tr key={keyOf(h)} className={`border-t border-slate-200 align-top ${usable(h) ? '' : 'text-slate-500'}`}>
                            <td className="px-3 py-2">
                              <input
                                type="checkbox"
                                aria-label={`Use ${h.name}`}
                                disabled={!usable(h)}
                                checked={usable(h) && !s.off.includes(keyOf(h))}
                                onChange={() => setService(i, { off: toggle(s.off, keyOf(h)) })}
                              />
                            </td>
                            <td className="px-3 py-2 font-mono text-xs">{h.name}</td>
                            <td className="px-3 py-2">{h.in}</td>
                            <td className="px-3 py-2 text-xs">
                              {h.operations[0]}
                              {h.operations.length > 1 && ` and ${h.operations.length - 1} more`}
                            </td>
                            <td className="px-3 py-2">
                              <span className="block text-xs font-semibold uppercase tracking-wide">{KIND_LABEL[h.kind]}</span>
                              <span className="text-xs">{h.metric}</span>
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
              </section>
            );
          })}
        </>
      )}

      {step === 4 && (
        <fieldset>
          <p className="mb-3 text-sm text-slate-600">
            Optional. These watch each server itself rather than a component of the project. Leave them all off if you do not need them.
          </p>
          {checks.length === 0 ? (
            <p className="text-sm text-slate-500">
              The template offers none.
            </p>
          ) : (
            <div className="grid gap-2 sm:grid-cols-2">
              {checks.map((c: any) => (
                <Choice
                  key={c.name}
                  title={c.name}
                  hint={c.titles.join('; ')}
                  checked={components.includes(c.name)}
                  onChange={() => setComponents(toggle(components, c.name))}
                />
              ))}
            </div>
          )}
        </fieldset>
      )}

      {step === 5 && plan && (
        <>
          <Summary
            rows={[
              ['Project', `${form.project.trim()} / ${env}`],
              ['Destination', form.endpoint.trim()],
              ['Servers send through', hasCluster && form.ingress.trim() ? `The cluster's collector, at ${form.ingress.trim()}` : 'The destination directly'],
              ['Application stacks', appChosen ? offered.map(([, label]) => label).join(', ') : 'No applications'],
              ['Microservices', named.map((s) => s.name.trim()).join(', ') || 'None'],
              ['Enabling', SIGNALS.filter(([id]) => signals.includes(id)).map(([, label]) => label).join(', ') || 'Nothing chosen'],
              ['Components', ['server health', ...picked(software)].join(', ')],
              ['Additional capability', picked(checks).join(', ') || 'None'],
            ]}
          />
          <div className="rounded-xl border border-slate-200 p-3">
            <p className="mb-2 text-sm text-slate-600">
              The package holds the README with the whole install guide, the otelgenie.sh script and every file the guide refers to. It contains internal addresses but no secrets. The next step shows the same guide here.
            </p>
            <div className="flex flex-wrap gap-2">
              <Button onClick={() => download(plan.name, plan.archive, 'application/gzip')}>Download the complete package</Button>
              <Button kind="secondary" onClick={() => download('README.md', plan.readme, 'text/markdown')}>
                Download README.md only
              </Button>
            </div>
          </div>
        </>
      )}

      {step === 6 && plan && (
        <>
          <ol className="space-y-3">
            {plan.process.map((p: any, i: number) => (
              <li key={p.title} className="rounded-xl border border-slate-200 p-3">
                <h4 className="text-sm font-semibold">
                  {i + 1}. {p.title}
                </h4>
                <p className="mt-1 text-sm text-slate-600">{p.text}</p>
                {p.waiting && <p className="mt-2 text-sm font-medium text-amber-700">Waiting for the answer to the question above.</p>}
                {p.items?.length > 0 && (
                  <ul className="mt-2 list-disc space-y-1 pl-5 text-sm">
                    {p.items.map((it: string) => (
                      <li key={it}>{it}</li>
                    ))}
                  </ul>
                )}
                {(p.commands ?? []).map((c: any) => (
                  <Command key={c.where + c.text} where={c.where} text={c.text} manual={p.manual?.[c.text]} />
                ))}
                {/* A question that can only be answered once this step is done; the steps after it are rebuilt from the answer. */}
                {p.ask === 'ingress' && (
                  <div className="mt-3 rounded-xl border border-indigo-300 bg-indigo-50 p-3">
                    <Field label="Address of the Ingress" hint={p.ask_text}>
                      <input
                        type="url"
                        className={inputClass + ' max-w-md'}
                        placeholder="http://203.0.113.10"
                        value={form.ingress}
                        onChange={(e) => setForm({ ...form, ingress: e.target.value })}
                      />
                    </Field>
                    <div className="mt-2 flex flex-wrap gap-2">
                      <Button kind="secondary" disabled={busy || !form.ingress.trim()} onClick={() => goTo(step)}>
                        Update the steps below
                      </Button>
                      {!plan.process.some((x: any) => x.waiting) && (
                        <Button kind="secondary" onClick={() => download(plan.name, plan.archive, 'application/gzip')}>
                          Download the package again, now with the server files
                        </Button>
                      )}
                    </div>
                  </div>
                )}
              </li>
            ))}
          </ol>
          <fieldset className="rounded-xl border border-slate-200 p-3">
            <legend className="px-1 text-sm font-medium">Or let this page walk you through it</legend>
            <p className="mb-2 text-sm text-slate-600">
              The guided setup looks at one server or cluster at a time, adjusts the files to what it finds, and checks each step with you.
            </p>
            {hasCluster && (
              <div className="mb-3 grid gap-2 sm:grid-cols-2">
                <Choice type="radio" title="Start with a Kubernetes cluster" hint="Set up through kubectl, with the OpenTelemetry Operator." checked={clusterFirst} onChange={() => setClusterFirst(true)} />
                <Choice type="radio" title="Start with a server" hint="Set up over SSH, as a systemd service." checked={!clusterFirst} onChange={() => setClusterFirst(false)} />
              </div>
            )}
            <Field
              label={kind === 'k8s' ? 'First cluster to set up' : 'First server to set up'}
              hint={`Any short label, for example ${kind === 'k8s' ? 'prod-gke-1' : 'db-host-1'}. You can add more later.`}
            >
              <input required className={inputClass + ' max-w-md'} value={form.target} onChange={(e) => setForm({ ...form, target: e.target.value })} />
            </Field>
          </fieldset>
        </>
      )}

      <ErrorNote message={error} />
    </Wizard>
  );
}
