// The engine runs inside the otelgenie program that serves this page. Every
// request goes to that program, on this machine, and nowhere else.

// Empty when the program serves the page. Set NEXT_PUBLIC_OTELGENIE_API to its
// address when working on the page with a development server.
export const API = process.env.NEXT_PUBLIC_OTELGENIE_API ?? '';

export async function post<T = any>(path: string, payload: unknown): Promise<T> {
  let res: Response;
  try {
    res = await fetch(API + path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload ?? {}), cache: 'no-store' });
  } catch {
    throw new Error('The otelgenie program is not answering. Start it again with "otelgenie serve" and reload this page.');
  }
  const body = await res.json().catch(() => null);
  if (!res.ok) throw new Error(body?.error ?? `The otelgenie program answered ${res.status}.`);
  return body as T;
}

export const call = <T = any>(method: string, payload: Record<string, unknown> = {}) => post<T>(`/api/call/${method}`, payload);

export const nowISO = () => new Date().toISOString();

export function download(name: string, base64: string, mime: string) {
  const bin = atob(base64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  const url = URL.createObjectURL(new Blob([bytes], { type: mime }));
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 10_000);
}

export async function fileToBase64(file: File): Promise<string> {
  const bytes = new Uint8Array(await file.arrayBuffer());
  let bin = '';
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(bin);
}
