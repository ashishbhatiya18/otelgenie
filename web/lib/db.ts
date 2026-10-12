// Everything the page remembers is kept by the otelgenie program, in files on
// this machine. Nothing is kept in the browser.

import { post } from './engine';

export type JourneyMeta = {
  id: string;
  project: string;
  env: string;
  commit: string;
  created: string;
  updated: string;
  summary: string;
};

// A project whose questions are not all answered yet. It becomes a journey
// when the guided setup starts.
export type Draft = {
  id: string;
  project: string;
  env: string;
  step: number;
  created: string;
  updated: string;
  // The form's own state, kept as the form wrote it.
  answers: any;
};

export type Delta = { events: any[] | null; blobs: Record<string, string> | null };

const store = <T = any>(op: string, payload: Record<string, unknown> = {}) => post<T>(`/api/store/${op}`, payload);

export const db = {
  get: async <T = any>(key: string) => ((await store('get', { key })).value ?? undefined) as T | undefined,
  set: (key: string, value: unknown) => store('set', { key, value }),

  putSnapshot: (snapshot: any) => store('putSnapshot', { snapshot }),
  getSnapshot: async (commit: string) => (await store('getSnapshot', { commit })).snapshot ?? undefined,

  listDrafts: async () => ((await db.get<Draft[]>('drafts')) ?? []).sort((a, b) => (a.updated < b.updated ? 1 : -1)),
  saveDraft: async (draft: Draft) => {
    const all = (await db.get<Draft[]>('drafts')) ?? [];
    await db.set('drafts', [...all.filter((d) => d.id !== draft.id), draft]);
  },
  deleteDraft: async (id: string) => {
    const all = (await db.get<Draft[]>('drafts')) ?? [];
    await db.set('drafts', all.filter((d) => d.id !== id));
  },

  listJourneys: async () => (await store<{ journeys: JourneyMeta[] }>('listJourneys')).journeys,

  // The program writes the events, the files and the summary in one step: a
  // step of a project is either fully stored or not stored at all. It answers
  // only once that is on disk.
  appendDelta: (meta: JourneyMeta, delta: Delta) => store('appendDelta', { meta, delta }),

  loadJourney: (id: string) => store<{ meta: JourneyMeta; events: any[]; blobs: Record<string, string> }>('loadJourney', { id }),

  deleteJourney: (id: string) => store('deleteJourney', { id }),
};
