import { useEffect, useRef, useState } from 'react';
import { Button, inputClass } from './ui';

export type ComponentInfo = { name: string; signals: string[]; titles: string[]; category?: string };

const OTHER = 'Other';

function Row({ c }: { c: ComponentInfo }) {
  return (
    <span className="block">
      <span className="font-medium">{c.name}</span>
      {(c.signals ?? []).map((s) => (
        <span key={s} className="ml-2 rounded bg-slate-200 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-slate-600">
          {s}
        </span>
      ))}
      <span className="mt-0.5 block text-xs text-slate-500">{c.titles.join('; ')}</span>
    </span>
  );
}

// Components are picked the way start.spring.io picks dependencies: a search
// dialog adds them, and what has been chosen is listed with a way to remove it.
export function ComponentPicker({ all, selected, onChange }: { all: ComponentInfo[]; selected: string[]; onChange: (names: string[]) => void }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [query, setQuery] = useState('');

  const q = query.trim().toLowerCase();
  const chosen = all.filter((c) => selected.includes(c.name));
  const matches = all.filter((c) => !selected.includes(c.name) && [c.name, c.category ?? '', ...c.titles, ...(c.signals ?? [])].join(' ').toLowerCase().includes(q));
  // Listed under their category, as the template names it; uncategorised ones last.
  const groups = Array.from(new Set(matches.map((c) => c.category || OTHER))).sort((a, b) => (a === OTHER ? 1 : b === OTHER ? -1 : a.localeCompare(b)));

  const open = () => {
    setQuery('');
    dialog.current?.showModal();
  };
  const add = (name: string, keepOpen: boolean) => {
    onChange([...selected, name]);
    if (!keepOpen) dialog.current?.close();
  };

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'b' && !dialog.current?.open) {
        e.preventDefault();
        open();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  return (
    <div>
      <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
        <div>
          <div className="text-sm font-medium">Components in this project</div>
          <p className="text-xs text-slate-500">The health of each server itself is always collected.</p>
        </div>
        <Button kind="secondary" onClick={open}>
          Add components… <span className="ml-1 text-xs text-slate-500">Ctrl/⌘ + B</span>
        </Button>
      </div>

      {chosen.length === 0 ? (
        <p className="rounded-xl border border-dashed border-slate-300 p-4 text-center text-sm text-slate-500">No component selected</p>
      ) : (
        <ul className="divide-y divide-slate-200 rounded-xl border border-slate-200">
          {chosen.map((c) => (
            <li key={c.name} className="flex items-start justify-between gap-3 p-3 text-sm">
              <span>
                {c.category && <span className="mb-0.5 block text-[10px] font-semibold uppercase tracking-wide text-indigo-700">{c.category}</span>}
                <Row c={c} />
              </span>
              <button
                type="button"
                aria-label={`Remove ${c.name}`}
                className="shrink-0 rounded px-2 text-lg leading-6 text-slate-500 hover:bg-slate-100 hover:text-red-700"
                onClick={() => onChange(selected.filter((n) => n !== c.name))}
              >
                ×
              </button>
            </li>
          ))}
        </ul>
      )}

      <dialog
        ref={dialog}
        aria-label="Add components"
        className="m-auto w-[calc(100%-2rem)] max-w-xl rounded-lg border border-slate-200 bg-white p-0 text-slate-900 shadow-xl backdrop:bg-slate-900/50"
        // A click that lands on the dialog itself, not its contents, is on the backdrop.
        onClick={(e) => e.target === dialog.current && dialog.current?.close()}
      >
        <div className="p-4">
          <input
            autoFocus
            className={inputClass}
            placeholder="Search: database, kubernetes, nginx, logs, …"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              // The dialog sits inside the step's form; Enter must not move to the next step.
              if (e.key !== 'Enter') return;
              e.preventDefault();
              if (matches[0]) add(matches[0].name, e.ctrlKey || e.metaKey);
            }}
          />
          <p className="mt-1 text-xs text-slate-500">Enter adds the first match. Hold Ctrl or ⌘ while choosing to add several.</p>
        </div>
        <ul className="max-h-80 overflow-y-auto border-t border-slate-200">
          {groups.map((g) => (
            <li key={g}>
              <div className="bg-slate-100 px-4 py-1.5 text-[11px] font-semibold uppercase tracking-wide text-slate-600">{g}</div>
              <ul>
                {matches
                  .filter((c) => (c.category || OTHER) === g)
                  .map((c) => (
                    <li key={c.name}>
                      <button
                        type="button"
                        className="block w-full px-4 py-3 text-left text-sm hover:bg-indigo-50 focus:bg-indigo-50 focus:outline-none"
                        onClick={(e) => add(c.name, e.ctrlKey || e.metaKey)}
                      >
                        <Row c={c} />
                      </button>
                    </li>
                  ))}
              </ul>
            </li>
          ))}
          {matches.length === 0 && <li className="px-4 py-6 text-center text-sm text-slate-500">{q ? 'Nothing matches.' : 'Everything is already added.'}</li>}
        </ul>
      </dialog>
    </div>
  );
}
