import { ReactNode, useState } from 'react';

export function Card({ title, children, tone }: { title?: string; children: ReactNode; tone?: 'plain' | 'error' | 'warn' | 'ok' }) {
  const tones = {
    plain: 'border-slate-200/80 bg-white',
    error: 'border-red-200 bg-red-50',
    warn: 'border-amber-200 bg-amber-50',
    ok: 'border-emerald-200 bg-emerald-50',
  };
  return (
    <section className={`rounded-2xl border p-5 shadow-sm shadow-slate-200/60 sm:p-6 ${tones[tone ?? 'plain']}`}>
      {title && <h3 className="mb-3 text-base font-semibold tracking-tight">{title}</h3>}
      {children}
    </section>
  );
}

export function Button({
  children,
  onClick,
  kind = 'primary',
  disabled,
  type = 'button',
}: {
  children: ReactNode;
  onClick?: () => void;
  kind?: 'primary' | 'secondary' | 'danger';
  disabled?: boolean;
  type?: 'button' | 'submit';
}) {
  const kinds = {
    primary:
      'bg-linear-to-b from-indigo-500 to-indigo-600 text-white shadow-sm shadow-indigo-600/30 hover:from-indigo-500 hover:to-indigo-500 disabled:from-slate-300 disabled:to-slate-300 disabled:shadow-none',
    secondary: 'border border-slate-300 bg-white text-slate-700 shadow-xs hover:border-slate-400 hover:bg-slate-50 disabled:text-slate-400',
    danger: 'border border-red-200 bg-white text-red-700 hover:bg-red-50',
  };
  return (
    <button
      type={type}
      onClick={onClick}
      disabled={disabled}
      className={`rounded-lg px-3.5 py-2 text-sm font-medium transition focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-indigo-500 disabled:cursor-not-allowed ${kinds[kind]}`}
    >
      {children}
    </button>
  );
}

export const inputClass =
  'w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-xs outline-none transition placeholder:text-slate-400 focus:border-indigo-500 focus:ring-4 focus:ring-indigo-100';

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="block">
      <span className="mb-1.5 block text-sm font-medium text-slate-700">{label}</span>
      {children}
      {hint && <span className="mt-1.5 block text-xs leading-relaxed text-slate-500">{hint}</span>}
    </label>
  );
}

// A checkbox drawn as a card, for picking several things from a short list.
export function Choice({
  title,
  hint,
  checked,
  onChange,
  type = 'checkbox',
}: {
  title: string;
  hint?: string;
  checked: boolean;
  onChange: () => void;
  type?: 'checkbox' | 'radio';
}) {
  return (
    <label className="flex cursor-pointer items-start gap-2.5 rounded-xl border border-slate-200 bg-white p-3 text-sm shadow-xs transition hover:border-slate-300 has-[:checked]:border-indigo-500 has-[:checked]:bg-indigo-50/70 has-[:checked]:ring-2 has-[:checked]:ring-indigo-100">
      <input type={type} className="mt-0.5 accent-indigo-600" checked={checked} onChange={onChange} />
      <span>
        <span className="font-medium">{title}</span>
        {hint && <span className="block text-xs text-slate-500">{hint}</span>}
      </span>
    </label>
  );
}

export function Summary({ rows }: { rows: [string, string][] }) {
  return (
    <dl className="grid gap-x-4 gap-y-1 text-sm sm:grid-cols-[12rem_1fr]">
      {rows.map(([label, value]) => (
        <div key={label} className="contents">
          <dt className="text-slate-500">{label}</dt>
          <dd className="mb-1 break-all font-medium sm:mb-0">{value}</dd>
        </div>
      ))}
    </dl>
  );
}

// A form split into named steps. Each step is submitted on its own, so the
// browser's required-field checks apply to exactly what is on screen.
export function Wizard({
  steps,
  step,
  onStep,
  onFinish,
  finish,
  busy,
  onCancel,
  children,
}: {
  steps: string[];
  step: number;
  onStep: (step: number) => void;
  onFinish: () => void;
  finish: string;
  busy?: boolean;
  onCancel?: () => void;
  children: ReactNode;
}) {
  const last = step === steps.length - 1;
  return (
    <form
      className="space-y-4"
      onSubmit={(e) => {
        e.preventDefault();
        if (last) onFinish();
        else onStep(step + 1);
      }}
    >
      <ol className="flex flex-wrap items-center gap-x-1 gap-y-2 rounded-xl bg-slate-50 p-1.5 text-sm ring-1 ring-slate-200/70">
        {steps.map((name, i) => (
          <li key={name}>
            {/* Only earlier steps can be jumped to; going forward is always through Next. */}
            <button
              type="button"
              disabled={busy || i >= step}
              aria-current={i === step ? 'step' : undefined}
              onClick={() => onStep(i)}
              className={`flex items-center gap-2 rounded-lg px-2.5 py-1.5 transition ${
                i === step ? 'bg-white font-semibold text-slate-900 shadow-sm ring-1 ring-slate-200' : i < step ? 'text-indigo-700 hover:bg-white' : 'text-slate-400'
              }`}
            >
              <span
                className={`flex h-5 w-5 items-center justify-center rounded-full text-[11px] font-semibold ${
                  i === step ? 'bg-indigo-600 text-white' : i < step ? 'bg-indigo-100 text-indigo-700' : 'bg-slate-200 text-slate-500'
                }`}
              >
                {i < step ? '✓' : i + 1}
              </span>
              {name}
            </button>
          </li>
        ))}
      </ol>
      <div key={step} className="space-y-4">
        {children}
      </div>
      <div className="flex gap-2 border-t border-slate-100 pt-4">
        {step > 0 && (
          <Button kind="secondary" onClick={() => onStep(step - 1)} disabled={busy}>
            Back
          </Button>
        )}
        <Button type="submit" disabled={busy}>
          {last ? finish : 'Next'}
        </Button>
        {onCancel && (
          <Button kind="secondary" onClick={onCancel} disabled={busy}>
            Cancel
          </Button>
        )}
      </div>
    </form>
  );
}

// A command to copy. When the same thing can be done without otelgenie.sh,
// manual holds those commands and a switch chooses which are shown.
export function Command({ text, where, manual }: { text: string; where?: string; manual?: string }) {
  const [copied, setCopied] = useState(false);
  const [byHand, setByHand] = useState(false);
  const shown = byHand && manual ? manual : text;
  const tab = (on: boolean) =>
    `rounded-md px-2 py-0.5 text-[11px] font-medium transition ${on ? 'bg-white text-slate-900 shadow-sm ring-1 ring-slate-200' : 'text-slate-500 hover:text-slate-800'}`;
  return (
    <div className="my-2">
      <div className="mb-1 flex flex-wrap items-center justify-between gap-2">
        {where ? <div className="text-xs font-semibold uppercase tracking-wide text-slate-500">{where}</div> : <span />}
        {manual && (
          <div role="group" aria-label="How to run this" className="flex gap-0.5 rounded-lg bg-slate-100 p-0.5">
            <button type="button" aria-pressed={!byHand} className={tab(!byHand)} onClick={() => setByHand(false)}>
              otelgenie.sh
            </button>
            <button type="button" aria-pressed={byHand} className={tab(byHand)} onClick={() => setByHand(true)}>
              By hand
            </button>
          </div>
        )}
      </div>
      <div className="flex items-start gap-2 rounded-xl bg-slate-900 p-3 shadow-sm">
        <pre className="flex-1 overflow-x-auto whitespace-pre-wrap break-all font-mono text-xs text-slate-100">{shown}</pre>
        <button
          type="button"
          className="shrink-0 rounded border border-slate-600 px-2 py-1 text-xs text-slate-200 hover:bg-slate-700"
          onClick={async () => {
            await navigator.clipboard.writeText(shown);
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          }}
        >
          {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
      {byHand && manual && (
        <p className="mt-1 text-xs text-slate-500">These are the commands the script runs. By hand there is no check first and no confirmation, so read each before running it.</p>
      )}
    </div>
  );
}

export function ErrorNote({ message }: { message: string }) {
  if (!message) return null;
  return (
    <p role="alert" className="whitespace-pre-wrap rounded-xl border border-red-200 bg-red-50 p-3 text-sm text-red-800">
      {message}
    </p>
  );
}

export function when(iso: string): string {
  const then = new Date(iso).getTime();
  if (!then) return '';
  const hours = (Date.now() - then) / 3_600_000;
  if (hours < 1) return 'less than an hour ago';
  if (hours < 48) return `${Math.floor(hours)} hours ago`;
  return `${Math.floor(hours / 24)} days ago`;
}
