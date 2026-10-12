import { API } from './engine';

// The otelgenie program that serves the page also holds the template.

export type CliTemplate = {
  snapshot: any;
  source: { url: string; ref: string; commit: string; updated: string };
  // A folder on this machine, read anew on every load.
  live: boolean;
  problems: string[] | null;
};

// template: what to use. hint: the program is there but has no template yet.
export type Cli = { template?: CliTemplate; hint?: string };

// Returns null when the page was not opened through the program.
export async function fromCli(): Promise<Cli | null> {
  try {
    const res = await fetch(API + '/api/template', { cache: 'no-store' });
    if (!(res.headers.get('content-type') ?? '').includes('application/json')) return null;
    const body = await res.json();
    return res.ok ? { template: body } : { hint: body.hint ?? 'The template could not be read.' };
  } catch {
    return null;
  }
}
