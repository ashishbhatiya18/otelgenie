import { useEffect, useRef, useState } from 'react';
import { CliTemplate } from '../lib/cli';
import { call } from '../lib/engine';
import { Profile } from './Setup';
import { Button, inputClass } from './ui';

// The one-time setup and its fingerprint, kept out of the way behind a gear in the page header.
export function SetupMenu({ profile, snapshot, cli, onSetup }: { profile: Profile; snapshot: any; cli?: CliTemplate; onSetup: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [info, setInfo] = useState<any>(null);
  const [fp, setFp] = useState<any>(null);
  const [other, setOther] = useState('');
  const [verdict, setVerdict] = useState('');

  useEffect(() => {
    (async () => {
      setFp(await call('fingerprint', { profile, snapshot }));
      setInfo(await call('describeTemplate', { snapshot }));
    })().catch(() => setFp(null));
  }, [profile, snapshot]);

  if (!fp) return null;

  async function compare() {
    const res = await call('compare', { text: fp.text, other });
    if (res.same) setVerdict('Same setup.');
    else if (res.code_only) setVerdict('Different setup. Paste their full fingerprint line to see which part differs.');
    else setVerdict('Different in: ' + res.diff.join(', ') + '.');
  }

  const differs = fp.has_reference && fp.reference_diff?.length > 0;

  return (
    <>
      <button
        type="button"
        aria-label={differs ? 'Setup (differs from your team’s reference)' : 'Setup'}
        title="Setup"
        className="relative rounded-xl p-1.5 text-slate-500 hover:bg-slate-100 hover:text-slate-900"
        onClick={() => dialog.current?.showModal()}
      >
        <svg viewBox="0 0 24 24" className="h-5 w-5" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <circle cx="12" cy="12" r="3" />
          <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09a1.65 1.65 0 0 0-1-1.51 1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09a1.65 1.65 0 0 0 1.51-1 1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33h0a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51h0a1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82v0a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z" />
        </svg>
        {differs && <span className="absolute right-0.5 top-0.5 h-2 w-2 rounded-full bg-amber-500" />}
      </button>

      <dialog
        ref={dialog}
        aria-label="Setup"
        className="m-auto w-[calc(100%-2rem)] max-w-xl rounded-lg border border-slate-200 bg-white p-0 text-slate-900 shadow-xl backdrop:bg-slate-900/50"
        // A click that lands on the dialog itself, not its contents, is on the backdrop.
        onClick={(e) => e.target === dialog.current && dialog.current?.close()}
      >
        <div className="p-4">
          <div className="mb-2 flex items-center justify-between gap-2">
            <h3 className="text-sm font-semibold">Setup fingerprint</h3>
            <button
              type="button"
              aria-label="Close"
              className="rounded px-2 text-lg leading-6 text-slate-500 hover:bg-slate-100"
              onClick={() => dialog.current?.close()}
            >
              ×
            </button>
          </div>
          <p className="font-mono text-lg font-semibold tracking-wider">{fp.fingerprint.code}</p>
          <p className="mt-1 break-all font-mono text-xs text-slate-600">{fp.text}</p>
          <p className="mt-2 text-sm text-slate-600">
            Read the short code to a colleague. If theirs is the same, you both did the one-time setup the same way.
          </p>
          {differs && (
            <p className="mt-2 text-sm font-medium text-amber-700">
              Your setup differs from your team&apos;s reference in: {fp.reference_diff.join(', ')}. Check those answers.
            </p>
          )}
          <div className="mt-3 flex items-center gap-2">
            <input className={inputClass} placeholder="Paste a colleague's fingerprint to compare" value={other} onChange={(e) => setOther(e.target.value)} />
            <Button kind="secondary" onClick={compare} disabled={!other.trim()}>
              Compare
            </Button>
          </div>
          {verdict && <p className="mt-2 text-sm font-medium">{verdict}</p>}
          <p className="mt-3 text-xs text-slate-500">
            Template {snapshot.repo} at {snapshot.commit.slice(0, 10)}, collector {info?.manifest?.collector_version}
          </p>
          {cli && (
            <p className="mt-1 text-xs text-slate-500">
              {cli.live
                ? 'The template is a folder on this machine, read anew every time the page loads.'
                : `Fetched ${new Date(cli.source.updated).toLocaleString()}. For the latest, run "otelgenie update" in a terminal and reload.`}
            </p>
          )}
          <div className="mt-3 border-t border-slate-200 pt-3">
            <Button
              kind="secondary"
              onClick={() => {
                dialog.current?.close();
                onSetup();
              }}
            >
              Change setup
            </Button>
          </div>
        </div>
      </dialog>
    </>
  );
}
