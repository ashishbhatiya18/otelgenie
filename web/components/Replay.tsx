import { useEffect, useState } from 'react';
import { db } from '../lib/db';
import { call, download, fileToBase64 } from '../lib/engine';
import { Profile } from './Setup';
import { Button, Card, ErrorNote, Field, inputClass } from './ui';

const BADGE: Record<string, string> = {
  problem: 'bg-red-600 text-white',
  warning: 'bg-amber-600 text-white',
  info: 'bg-slate-500 text-white',
};

// Walks through a recorded session: the project it was opened from, or a
// session file someone else saved. Either is read on this machine and goes
// nowhere; the project itself is not changed.
export function Replay({ myFingerprint, journeyId, profile, onBack }: { myFingerprint: string; journeyId?: string; profile?: Profile; onBack: () => void }) {
  const [code, setCode] = useState('');
  const [result, setResult] = useState<any>(null);
  const [error, setError] = useState('');
  const [at, setAt] = useState(0);

  useEffect(() => {
    if (!journeyId || !profile) return;
    (async () => {
      const saved = await db.loadJourney(journeyId);
      const snapshot = await db.getSnapshot(saved.meta.commit);
      if (!snapshot) throw new Error('The template this project started with is no longer stored on this machine.');
      await call('open', { id: journeyId, profile, snapshot, events: saved.events, blobs: saved.blobs });
      const own = await call('export', { id: journeyId, now: new Date().toISOString(), browser: navigator.userAgent });
      setResult(await call('analyze', { bundle: own.base64, reviewer: myFingerprint, code: own.session_code }));
    })().catch((e) => setError(e.message ?? String(e)));
  }, [journeyId, profile, myFingerprint]);

  async function load(file: File | undefined) {
    if (!file) return;
    setError('');
    setResult(null);
    try {
      setResult(await call('analyze', { bundle: await fileToBase64(file), reviewer: myFingerprint, code }));
      setAt(0);
    } catch (e: any) {
      setError(e.message ?? String(e));
    }
  }

  const r = result?.report;
  const step = r?.steps?.[at];

  return (
    <div className="space-y-4">
      <div>
        <button className="text-sm text-indigo-600 hover:underline" onClick={onBack}>
          ← Back
        </button>
        <h2 className="text-xl font-semibold">Debug session</h2>
        {journeyId && <p className="text-xs text-slate-500">Showing this project&apos;s own session. Open a file below to look at someone else&apos;s instead.</p>}
      </div>

      <Card title="Helping someone else?">
        <p className="mb-3 text-sm text-slate-600">
          Open the session file they sent you to walk through their whole session and get a PDF of where it went wrong.
        </p>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Session code they read out (optional)" hint="If it matches, the file is complete and unedited.">
            <input className={inputClass + ' font-mono'} placeholder="XXXX-XXXX" value={code} onChange={(e) => setCode(e.target.value)} />
          </Field>
          <Field label="Session file">
            <input type="file" accept=".zip" className="block w-full text-sm" onChange={(e) => load(e.target.files?.[0])} />
          </Field>
        </div>
      </Card>

      <ErrorNote message={error} />

      {r && (
        <>
          <Card tone={r.verdict.startsWith('Every server') ? 'ok' : 'error'} title="Where it went wrong">
            <p className="text-sm font-medium">{r.verdict}</p>
            <ul className="mt-2 text-sm">
              {(r.targets ?? []).map((t: any) => (
                <li key={t.name}>
                  <span className="font-medium">{t.name}</span>: {t.status}
                </li>
              ))}
            </ul>
            <div className="mt-3">
              <Button onClick={() => download(result.pdf_name, result.pdf, 'application/pdf')}>Download the PDF report</Button>
            </div>
          </Card>

          <Card title="About this recording">
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
              <dt className="text-slate-500">Session code</dt>
              <dd className="font-mono">{r.meta.session_code}</dd>
              <dt className="text-slate-500">Integrity</dt>
              <dd>{!r.chain_ok ? 'Damaged: ' + r.chain_problem : !r.code_matches ? 'Does not match the code given' : 'Complete and unedited'}</dd>
              <dt className="text-slate-500">Re-run of their inputs</dt>
              <dd>
                {r.rerun.error
                  ? 'Could not be re-run: ' + r.rerun.error
                  : r.rerun.same
                    ? 'Identical. The tool did what it was built to do, so look at the inputs and the server.'
                    : `Different in ${r.rerun.diffs.length} place(s). This points at the tool or a version difference.`}
              </dd>
              <dt className="text-slate-500">They provided</dt>
              <dd>
                {r.inputs.project} / {r.inputs.env}, {r.inputs.endpoint}; {r.inputs.signals?.join(', ')}; {r.inputs.components?.join(', ')}
              </dd>
              <dt className="text-slate-500">Their setup</dt>
              <dd className="break-all font-mono text-xs">
                {r.meta.fingerprint.code}
                {r.reviewer_diff?.length > 0 && <span className="ml-2 font-sans text-amber-700"> differs from yours in: {r.reviewer_diff.join(', ')}</span>}
              </dd>
            </dl>
          </Card>

          <Card title={`Findings (${r.findings?.length ?? 0})`}>
            {(r.findings ?? []).length === 0 && <p className="text-sm">Nothing unusual was recorded.</p>}
            <ul className="space-y-2">
              {(r.findings ?? []).map((f: any, i: number) => (
                <li key={i} className="text-sm">
                  <span className={`mr-2 rounded px-2 py-0.5 text-xs font-semibold uppercase ${BADGE[f.severity]}`}>{f.severity}</span>
                  <span className="font-medium">{f.title}</span>
                  {f.target && <span className="text-slate-500"> [{f.target}]</span>}
                  {f.seq > 0 && (
                    <button
                      className="ml-2 text-indigo-600 hover:underline"
                      onClick={() => {
                        const idx = r.steps.reduce((best: number, s: any, k: number) => (s.seq <= f.seq ? k : best), 0);
                        setAt(idx);
                        document.getElementById('stepper')?.scrollIntoView({ behavior: 'smooth' });
                      }}
                    >
                      show
                    </button>
                  )}
                  {f.detail && <pre className="mt-1 whitespace-pre-wrap break-words text-xs text-slate-600">{f.detail}</pre>}
                </li>
              ))}
            </ul>
          </Card>

          {step && (
            <div id="stepper">
              <Card title={`Step by step: ${at + 1} of ${r.steps.length}`}>
                <div className="mb-3 flex gap-2">
                  <Button kind="secondary" disabled={at === 0} onClick={() => setAt(at - 1)}>
                    Previous
                  </Button>
                  <Button kind="secondary" disabled={at >= r.steps.length - 1} onClick={() => setAt(at + 1)}>
                    Next
                  </Button>
                </div>
                {step.gap && <p className="mb-2 text-center text-xs italic text-slate-500">— {step.gap} —</p>}
                <p className="font-medium">
                  {step.action}
                  {step.target && <span className="text-slate-500"> [{step.target}]</span>}
                </p>
                <p className="text-xs text-slate-500">
                  {new Date(step.ts).toLocaleString()} · event {step.seq}
                </p>
                {step.input && (
                  <>
                    <div className="mt-3 text-xs font-semibold uppercase text-slate-500">What they entered</div>
                    <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all rounded bg-slate-100 p-2 font-mono text-xs">{step.input}</pre>
                  </>
                )}
                {step.rejected && <p className="mt-2 text-sm font-medium text-red-700">Not accepted: {step.rejected}</p>}
                {(step.decisions ?? []).length > 0 && (
                  <ul className="mt-2 list-disc pl-5 text-sm">
                    {step.decisions.map((d: string, i: number) => (
                      <li key={i}>{d}</li>
                    ))}
                  </ul>
                )}
                {step.outcome && <p className="mt-2 text-sm font-medium">{step.outcome}</p>}
                {(step.files ?? []).length > 0 && (
                  <p className="mt-2 text-xs text-slate-500">Files: {step.files.map((f: any) => `${f.path} (${f.change})`).join(', ')}</p>
                )}
                {step.saw && (
                  <>
                    <div className="mt-3 text-xs font-semibold uppercase text-slate-500">What they were shown next</div>
                    <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words rounded bg-indigo-50 p-2 font-mono text-xs">{step.saw}</pre>
                  </>
                )}
              </Card>
            </div>
          )}
        </>
      )}
    </div>
  );
}
