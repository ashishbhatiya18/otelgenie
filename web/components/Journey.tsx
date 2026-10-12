import { useCallback, useEffect, useState } from 'react';
import { db, JourneyMeta } from '../lib/db';
import { call, download, nowISO } from '../lib/engine';
import { Profile } from './Setup';
import { Button, Card, Command, ErrorNote, inputClass, when } from './ui';

const STEP_NAMES: Record<string, string> = {
  discover: 'Look',
  confirm: 'Confirm',
  review: 'Review',
  apply: 'Install',
  verify: 'Check',
  done: 'Done',
};
const STEPS = Object.keys(STEP_NAMES);
const RESUME_AFTER_MS = 10 * 60 * 1000;

function summarise(state: any): string {
  return (state.targets ?? []).map((t: any) => `${t.name}: ${t.step === 'done' ? 'finished' : 'at ' + STEP_NAMES[t.step]}`).join(', ') || 'Nothing set up yet';
}

export function Journey({ id, profile, onBack, onDebug }: { id: string; profile: Profile; onBack: () => void; onDebug: () => void }) {
  const [meta, setMeta] = useState<JourneyMeta | null>(null);
  const [state, setState] = useState<any>(null);
  const [active, setActive] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState('');
  const [newServer, setNewServer] = useState('');
  const [newKind, setNewKind] = useState('vm');
  const [exported, setExported] = useState('');
  const [recap, setRecap] = useState('');

  // Every action is written to IndexedDB before the next screen is shown.
  const act = useCallback(
    async (action: any, m: JourneyMeta) => {
      setBusy(true);
      setError('');
      try {
        const res = await call('act', { id, action, now: nowISO() });
        const next = { ...m, updated: nowISO(), summary: summarise(res.state) };
        await db.appendDelta(next, res.delta);
        setMeta(next);
        setState(res.state);
        return res.state;
      } catch (e: any) {
        setError(e.message ?? String(e));
      } finally {
        setBusy(false);
      }
    },
    [id],
  );

  useEffect(() => {
    (async () => {
      const saved = await db.loadJourney(id);
      const snapshot = await db.getSnapshot(saved.meta.commit);
      if (!snapshot) throw new Error('The template this journey started with is no longer stored on this machine.');
      const opened = await call('open', { id, profile, snapshot, events: saved.events, blobs: saved.blobs });
      setMeta(saved.meta);
      setState(opened.state);
      setActive(opened.state.targets?.[0]?.name ?? '');
      const idle = Date.now() - new Date(opened.state.last_event).getTime();
      if (idle > RESUME_AFTER_MS) {
        setRecap(`Welcome back. You were last here ${when(opened.state.last_event)}. ${summarise(opened.state)}.`);
        await act({ type: 'resume' }, saved.meta);
      }
    })().catch((e) => setError(e.message ?? String(e)));
  }, [id, profile, act]);

  if (!state || !meta) return <ErrorNote message={error} />;
  const view = state.views?.find((v: any) => v.target === active);
  const target = state.targets?.find((t: any) => t.name === active);

  async function downloadFiles() {
    const res = await call('archive', { id, target: active });
    download(res.name, res.base64, 'application/gzip');
  }

  async function exportBundle() {
    const res = await call('export', { id, now: nowISO(), browser: navigator.userAgent });
    download(res.name, res.base64, 'application/zip');
    setExported(res.session_code);
  }

  return (
    <div className="space-y-4">
      <div>
        <button className="text-sm text-indigo-600 hover:underline" onClick={onBack}>
          ← All projects
        </button>
        <h2 className="text-xl font-semibold">
          {state.inputs.project} / {state.inputs.env}
        </h2>
        <p className="text-xs text-slate-500">
          Sending to {state.inputs.endpoint} · session code <span className="font-mono">{state.session_code}</span>
        </p>
      </div>

      {recap && <Card tone="ok">{recap}</Card>}

      <div className="flex flex-wrap items-center gap-2">
        {state.targets.map((t: any) => (
          <button
            key={t.name}
            onClick={() => setActive(t.name)}
            className={`rounded-full border px-3 py-1 text-sm ${
              t.name === active
                ? 'border-indigo-600 bg-indigo-600 text-white'
                : 'border-slate-300 bg-white'
            }`}
          >
            {t.name} · {t.step === 'done' ? 'finished' : STEP_NAMES[t.step]}
          </button>
        ))}
        <form
          className="flex items-center gap-2"
          onSubmit={async (e) => {
            e.preventDefault();
            const next = await act({ type: 'add_target', data: { name: newServer, kind: newKind } }, meta);
            const label = newServer.trim().toLowerCase();
            if (next?.targets?.some((t: any) => t.name === label)) {
              setActive(label);
              setNewServer('');
            } else {
              setError('Use a short, unused label of lowercase letters, digits and dashes, for example db-host-2.');
            }
          }}
        >
          {state.kinds?.includes('k8s') && (
            <select aria-label="Kind of target" className={inputClass + ' w-28'} value={newKind} onChange={(e) => setNewKind(e.target.value)}>
              <option value="vm">Server</option>
              <option value="k8s">Cluster</option>
            </select>
          )}
          <input
            className={inputClass + ' w-44'}
            placeholder={newKind === 'k8s' ? 'Add another cluster' : 'Add another server'}
            value={newServer}
            onChange={(e) => setNewServer(e.target.value)}
          />
          <Button type="submit" kind="secondary" disabled={!newServer.trim() || busy}>
            Add
          </Button>
        </form>
      </div>

      {view && target && (
        <Card>
          <ol className="mb-4 flex flex-wrap gap-1 text-xs">
            {STEPS.map((s, i) => (
              <li
                key={s}
                className={`rounded px-2 py-1 ${
                  s === view.step
                    ? 'bg-indigo-600 font-semibold text-white'
                    : i < STEPS.indexOf(view.step)
                      ? 'bg-emerald-100 text-emerald-900'
                      : 'bg-slate-100 text-slate-500'
                }`}
              >
                {i + 1}. {STEP_NAMES[s]}
              </li>
            ))}
          </ol>
          <h3 className="text-lg font-semibold">{view.title}</h3>
          <p className="mt-1 text-sm text-slate-600">{view.why}</p>

          <div className="mt-4 space-y-3">
            {view.blocks?.map((b: any, i: number) => (
              <Block key={i} block={b} />
            ))}
          </div>

          {view.download && view.step !== 'done' && (
            <div className="mt-4">
              <Button kind="secondary" onClick={downloadFiles}>
                Download the files for {active}
              </Button>
            </div>
          )}

          <div className="mt-4">
            <StepForm
              key={`${active}-${view.step}-${state.events}`}
              view={view}
              busy={busy}
              onSubmit={(data) => act({ type: 'submit', target: active, step: view.step, data }, meta)}
            />
          </div>

          {STEPS.indexOf(view.step) > 0 && (
            <div className="mt-6 border-t border-slate-200 pt-3 text-sm">
              Need to change something?{' '}
              {['discover', 'confirm']
                .filter((s) => STEPS.indexOf(s) < STEPS.indexOf(view.step))
                .map((s) => (
                  <button
                    key={s}
                    className="mr-3 text-indigo-600 hover:underline"
                    onClick={() => act({ type: 'redo', target: active, step: s }, meta)}
                  >
                    Go back to “{STEP_NAMES[s]}”
                  </button>
                ))}
            </div>
          )}
        </Card>
      )}

      <ErrorNote message={error} />

      <div className="grid gap-4 md:grid-cols-2">
        <Card title="Notes">
          <p className="mb-2 text-xs text-slate-500">Anything worth remembering, such as who you asked and when. Notes are part of the session record.</p>
          <ul className="mb-2 space-y-1 text-sm">
            {(state.notes ?? []).map((n: any, i: number) => (
              <li key={i}>
                <span className="text-xs text-slate-500">{new Date(n.ts).toLocaleString()}:</span> {n.text}
              </li>
            ))}
          </ul>
          <form
            className="flex gap-2"
            onSubmit={async (e) => {
              e.preventDefault();
              await act({ type: 'note', data: { text: note } }, meta);
              setNote('');
            }}
          >
            <input className={inputClass} value={note} onChange={(e) => setNote(e.target.value)} placeholder="Add a note" />
            <Button type="submit" kind="secondary" disabled={!note.trim() || busy}>
              Add
            </Button>
          </form>
        </Card>

        <Card title="Stuck? Send your session to someone who can help">
          <p className="mb-2 text-sm text-slate-600">
            This saves one file with everything you entered and everything this page showed you. It contains server names and internal addresses
            but no passwords or tokens. It is also your backup: keep a copy if you will be away for more than a few days.
          </p>
          <div className="flex flex-wrap gap-2">
            <Button kind="secondary" onClick={exportBundle}>
              Save session file
            </Button>
            <Button kind="secondary" onClick={onDebug}>
              Debug session
            </Button>
          </div>
          {exported && (
            <p className="mt-2 text-sm">
              Saved. Tell them your session code so they can confirm the file is complete: <span className="font-mono font-semibold">{exported}</span>
            </p>
          )}
        </Card>
      </div>
    </div>
  );
}

function Block({ block }: { block: any }) {
  const items = block.items?.filter(Boolean) ?? [];
  const list = items.length > 0 && (
    <ul className="mt-1 list-disc space-y-1 pl-5 text-sm">
      {items.map((it: string, i: number) => (
        <li key={i} className="break-words">
          {it}
        </li>
      ))}
    </ul>
  );
  switch (block.kind) {
    case 'command':
      return <Command text={block.text} where={block.title} />;
    case 'ok':
    case 'warn':
    case 'error':
      return (
        <Card tone={block.kind} title={block.title}>
          {block.text && <p className="break-words text-sm">{block.text}</p>}
          {list}
        </Card>
      );
    case 'table':
      return (
        <div className="overflow-x-auto">
          {block.title && <div className="mb-1 text-sm font-semibold">{block.title}</div>}
          <table className="w-full text-left text-sm">
            <thead>
              <tr>
                {block.rows[0].map((h: string) => (
                  <th key={h} className="border-b border-slate-300 py-1 pr-4 font-semibold">
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {block.rows.slice(1).map((row: string[], i: number) => (
                <tr key={i}>
                  {row.map((c, k) => (
                    <td key={k} className="border-b border-slate-100 py-1 pr-4">
                      {c}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      );
    default:
      return (
        <div>
          {block.title && <div className="text-sm font-semibold">{block.title}</div>}
          {block.text && <p className="text-sm">{block.text}</p>}
          {list}
        </div>
      );
  }
}

function StepForm({ view, busy, onSubmit }: { view: any; busy: boolean; onSubmit: (data: any) => void }) {
  const form = view.form ?? { kind: 'none' };
  const [paste, setPaste] = useState('');
  const [picked, setPicked] = useState<Record<string, boolean>>(() =>
    Object.fromEntries((form.items ?? []).map((it: any) => [it.component, it.found])),
  );
  const [vars, setVars] = useState<Record<string, string>>(() =>
    Object.fromEntries((form.items ?? []).flatMap((it: any) => (it.fields ?? []).map((f: any) => [f.name, f.value ?? '']))),
  );

  if (form.kind === 'paste') {
    return (
      <div className="space-y-2">
        <label className="block text-sm font-medium">{form.label}</label>
        <textarea
          className={inputClass + ' h-40 font-mono text-xs'}
          spellCheck={false}
          value={paste}
          onChange={(e) => setPaste(e.target.value)}
          placeholder="Paste here"
        />
        <Button disabled={busy || !paste.trim()} onClick={() => onSubmit({ paste })}>
          Continue
        </Button>
      </div>
    );
  }
  if (form.kind === 'choice') {
    return (
      <div className="flex flex-col items-start gap-2">
        {form.options.map((o: any, i: number) => (
          <Button key={o.id} kind={i === 0 ? 'primary' : 'secondary'} disabled={busy} onClick={() => onSubmit({ choice: o.id })}>
            {o.label}
          </Button>
        ))}
      </div>
    );
  }
  if (form.kind === 'confirm') {
    return (
      <div className="space-y-3">
        {(form.items ?? []).map((it: any) => (
          <div key={it.component} className="rounded-xl border border-slate-200 p-3">
            <label className="flex items-start gap-2">
              <input
                type="checkbox"
                className="mt-1"
                checked={!!picked[it.component]}
                onChange={(e) => setPicked({ ...picked, [it.component]: e.target.checked })}
              />
              <span>
                <span className="font-medium">{it.component}</span>
                <span className="block text-xs text-slate-500">{it.titles.join('; ')}</span>
                <span className={`block text-sm ${it.found ? 'text-emerald-700' : 'text-amber-700'}`}>
                  {it.evidence}
                </span>
              </span>
            </label>
            {picked[it.component] &&
              (it.fields ?? []).map((f: any) => (
                <label key={f.name} className="mt-2 block pl-6 text-sm">
                  <span className="block">
                    {f.label}
                    {f.required && ' (needed)'}
                  </span>
                  <input className={inputClass} value={vars[f.name] ?? ''} onChange={(e) => setVars({ ...vars, [f.name]: e.target.value })} />
                  {f.hint && <span className="text-xs text-slate-500">{f.hint}</span>}
                </label>
              ))}
          </div>
        ))}
        <Button
          disabled={busy}
          onClick={() => {
            const shown = new Set(
              (form.items ?? []).filter((it: any) => picked[it.component]).flatMap((it: any) => (it.fields ?? []).map((f: any) => f.name)),
            );
            onSubmit({ components: picked, vars: Object.fromEntries(Object.entries(vars).filter(([k]) => shown.has(k))) });
          }}
        >
          Continue
        </Button>
      </div>
    );
  }
  return null;
}
