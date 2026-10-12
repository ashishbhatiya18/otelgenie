import Head from 'next/head';
import { useCallback, useEffect, useState } from 'react';
import { Home } from '../components/Home';
import { Journey } from '../components/Journey';
import { Replay } from '../components/Replay';
import { emptyProfile, Profile, Setup } from '../components/Setup';
import { SetupMenu } from '../components/SetupMenu';
import { Button, Card, Command, ErrorNote } from '../components/ui';
import { db } from '../lib/db';
import { Cli, fromCli } from '../lib/cli';
import { call } from '../lib/engine';

type Screen = { name: 'loading' } | { name: 'no-template' } | { name: 'setup' } | { name: 'home' } | { name: 'journey'; id: string } | { name: 'replay'; id?: string; back: Screen };

export default function Index() {
  const [screen, setScreen] = useState<Screen>({ name: 'loading' });
  const [profile, setProfile] = useState<Profile | null>(null);
  const [snapshot, setSnapshot] = useState<any>(null);
  const [fingerprint, setFingerprint] = useState('');
  // What the otelgenie program that serves the page says about its template.
  const [cli, setCli] = useState<Cli | null>(null);
  const [error, setError] = useState('');

  const refresh = useCallback(async () => {
    const served = await fromCli();
    setCli(served);
    if (!served) throw new Error('This page works through the otelgenie program. Start it with "otelgenie serve" and open the address it prints.');
    let p = await db.get<Profile>('profile');
    if (!served.template) {
      setScreen({ name: 'no-template' });
      return;
    }
    {
      // Whatever the program holds now is the template. A copy of each version is kept, because a project keeps the one it started with.
      await db.putSnapshot(served.template.snapshot);
      await db.set('commit', served.template.snapshot.commit);
      if (p && (p.template_repo !== served.template.source.url || p.template_ref !== served.template.source.ref)) {
        p = { ...p, template_repo: served.template.source.url, template_ref: served.template.source.ref };
        await db.set('profile', p);
      }
    }
    const commit = await db.get<string>('commit');
    const snap = commit ? await db.getSnapshot(commit) : null;
    if (!p || !snap) {
      setScreen({ name: 'setup' });
      return;
    }
    setProfile(p);
    setSnapshot(snap);
    setFingerprint((await call('fingerprint', { profile: p, snapshot: snap })).text);
    // Reopen whatever was on screen before a reload or restart.
    const last = await db.get<string>('open_journey');
    setScreen(last ? { name: 'journey', id: last } : { name: 'home' });
  }, []);

  useEffect(() => {
    refresh().catch((e) => setError(e.message ?? String(e)));
  }, [refresh]);

  const go = async (next: Screen) => {
    await db.set('open_journey', next.name === 'journey' ? next.id : '');
    setScreen(next);
  };

  return (
    <>
      <Head>
        <title>otelgenie</title>
        <meta name="viewport" content="width=device-width, initial-scale=1" />
      </Head>
      <header className="sticky top-0 z-10 border-b border-slate-200/70 bg-white/80 backdrop-blur">
        <div className="mx-auto flex max-w-4xl items-center justify-between gap-3 px-4 py-3">
          <div className="flex items-center gap-2.5">
            <span className="flex h-8 w-8 items-center justify-center rounded-xl bg-linear-to-br from-indigo-500 to-violet-600 text-white shadow-sm shadow-indigo-600/30">
              <svg viewBox="0 0 24 24" className="h-4.5 w-4.5" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                <path d="M12 3l1.8 4.7L18.5 9.5l-4.7 1.8L12 16l-1.8-4.7L5.5 9.5l4.7-1.8z" />
                <path d="M18.5 15.5l.7 1.8 1.8.7-1.8.7-.7 1.8-.7-1.8-1.8-.7 1.8-.7z" />
              </svg>
            </span>
            <div>
              <h1 className="text-lg font-semibold leading-tight tracking-tight">otelgenie</h1>
              <p className="text-xs leading-tight text-slate-500">From a collector endpoint to working telemetry</p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <p className="hidden items-center gap-1.5 rounded-full bg-emerald-50 px-2.5 py-1 text-xs font-medium text-emerald-800 ring-1 ring-emerald-200 sm:flex" title="What you enter is kept by the otelgenie program on this machine and sent nowhere else.">
              <span className="h-1.5 w-1.5 rounded-full bg-emerald-500" />
              Everything stays on this machine
            </p>
            {screen.name === 'home' && profile && snapshot && <SetupMenu profile={profile} snapshot={snapshot} cli={cli?.template} onSetup={() => setScreen({ name: 'setup' })} />}
          </div>
        </div>
      </header>
      <main className="mx-auto max-w-4xl space-y-4 px-4 py-6">
        <p className="text-xs text-slate-500 sm:hidden">Everything stays on this machine. Nothing you enter is sent anywhere else.</p>
        <ErrorNote message={error} />
        {screen.name === 'loading' && !error && <p className="text-sm text-slate-500">Starting…</p>}
        {screen.name === 'no-template' && (
          <Card title="There is no template yet" tone="warn">
            <p className="text-sm">{cli?.hint}</p>
            <p className="mt-2 text-sm text-slate-600">
              The template says what can be collected and how. It is kept by your engineering team; ask them for its address if you do not have it.
            </p>
            <Command where="In a terminal on this machine" text={'otelgenie config template-url ADDRESS-OF-THE-TEMPLATE\notelgenie update'} />
            <Button onClick={() => refresh().catch((e) => setError(e.message ?? String(e)))}>Try again</Button>
          </Card>
        )}
        {cli?.template?.problems && cli.template.problems.length > 0 && (
          <ErrorNote message={'The template has problems. Send this to its owner:\n' + cli.template.problems.join('\n')} />
        )}
        {screen.name === 'setup' && cli?.template && (
          <Setup initial={profile ?? emptyProfile} cli={cli!.template!} onDone={() => refresh()} onCancel={profile ? () => setScreen({ name: 'home' }) : undefined} />
        )}
        {screen.name === 'home' && profile && snapshot && (
          <Home
            profile={profile}
            snapshot={snapshot}
            onOpen={(id) => go({ name: 'journey', id })}
            onDebug={() => setScreen({ name: 'replay', back: { name: 'home' } })}
          />
        )}
        {screen.name === 'journey' && profile && (
          <Journey
            key={screen.id}
            id={screen.id}
            profile={profile}
            onBack={() => go({ name: 'home' })}
            onDebug={() => setScreen({ name: 'replay', id: screen.id, back: screen })}
          />
        )}
        {screen.name === 'replay' && profile && <Replay myFingerprint={fingerprint} journeyId={screen.id} profile={profile} onBack={() => setScreen(screen.back)} />}
      </main>
    </>
  );
}
