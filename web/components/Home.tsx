import { useEffect, useState } from 'react';
import { db, Draft, JourneyMeta } from '../lib/db';
import { call } from '../lib/engine';
import { NewProject, STEPS } from './NewProject';
import { Profile } from './Setup';
import { Button, Card, ErrorNote, when } from './ui';

export function Home({
  profile,
  snapshot,
  onOpen,
  onDebug,
}: {
  profile: Profile;
  snapshot: any;
  onOpen: (id: string) => void;
  onDebug: () => void;
}) {
  const [info, setInfo] = useState<any>(null);
  const [journeys, setJourneys] = useState<JourneyMeta[]>([]);
  const [drafts, setDrafts] = useState<Draft[]>([]);
  // The form is open on a new project or on a draft being carried on with.
  const [editing, setEditing] = useState<Draft | 'new' | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    (async () => {
      setInfo(await call('describeTemplate', { snapshot }));
      setJourneys(await db.listJourneys());
      setDrafts(await db.listDrafts());
    })().catch((e) => setError(e.message));
  }, [profile, snapshot]);

  async function closeForm() {
    setEditing(null);
    setDrafts(await db.listDrafts());
  }

  async function discard(d: Draft) {
    await db.deleteDraft(d.id);
    setDrafts(await db.listDrafts());
  }

  return (
    <div className="space-y-4">
      <ErrorNote message={error} />
      {editing ? (
        <Card title="Start enabling telemetry for a project">
          <NewProject
            key={editing === 'new' ? 'new' : editing.id}
            profile={profile}
            snapshot={snapshot}
            info={info}
            draft={editing === 'new' ? undefined : editing}
            onOpen={onOpen}
            onCancel={closeForm}
          />
        </Card>
      ) : (
        <Card>
          <div className="mb-3 flex items-center justify-between gap-2">
            <h3 className="text-base font-semibold tracking-tight">Projects</h3>
            <div className="flex gap-2">
              <Button kind="secondary" onClick={onDebug}>
                Debug session
              </Button>
              <Button onClick={() => setEditing('new')}>Add new</Button>
            </div>
          </div>
          {journeys.length + drafts.length === 0 ? (
            <p className="rounded-xl border border-dashed border-slate-300 bg-slate-50/60 px-4 py-8 text-center text-sm text-slate-500">
              No project yet. Add one to start enabling telemetry.
            </p>
          ) : (
            <ul className="divide-y divide-slate-100">
              {drafts.map((d) => (
                <li key={d.id} className="flex flex-wrap items-center justify-between gap-2 py-3">
                  <div>
                    <div className="font-medium">
                      {d.project} / {d.env}
                    </div>
                    <div className="text-xs text-slate-500">
                      Not finished: step {d.step + 1} of {STEPS.length} · last touched {when(d.updated)}
                    </div>
                  </div>
                  <div className="flex gap-2">
                    <Button kind="danger" onClick={() => discard(d)}>
                      Discard
                    </Button>
                    <Button kind="secondary" onClick={() => setEditing(d)}>
                      Continue
                    </Button>
                  </div>
                </li>
              ))}
              {journeys.map((j) => (
                <li key={j.id} className="flex flex-wrap items-center justify-between gap-2 py-3">
                  <div>
                    <div className="font-medium">
                      {j.project} / {j.env}
                    </div>
                    <div className="text-xs text-slate-500">
                      {j.summary} · last touched {when(j.updated)}
                    </div>
                  </div>
                  <Button kind="secondary" onClick={() => onOpen(j.id)}>
                    Continue
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </Card>
      )}
    </div>
  );
}
