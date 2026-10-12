import { CliTemplate } from '../lib/cli';
import { useState } from 'react';
import { db } from '../lib/db';
import { call } from '../lib/engine';
import { Card, Choice, Command, ErrorNote, Field, inputClass, Summary, Wizard } from './ui';

export type Profile = {
  template_repo: string;
  template_ref: string;
  package_method: string;
  package_repo_url: string;
  image_method: string;
  registry: string;
  ca_bundle: string;
  os_families: string[];
  platforms: string[];
  curl_image?: string;
};

export const emptyProfile: Profile = {
  template_repo: '',
  template_ref: '',
  package_method: 'manual',
  package_repo_url: '',
  image_method: 'none',
  registry: '',
  ca_bundle: '',
  os_families: ['rhel'],
  platforms: [],
  curl_image: '',
};

const STEPS = ['Template', 'Airgap', 'Servers'];

const OS_FAMILIES: [string, string][] = [
  ['rhel', 'RHEL, Rocky, Alma, Oracle Linux'],
  ['debian', 'Debian, Ubuntu'],
];

const PACKAGE_METHODS: Record<string, string> = {
  manual: 'Copying the file across by hand',
  repo: 'An internal package repository',
};

const IMAGE_METHODS: Record<string, string> = {
  none: 'Not applicable',
  mirror: 'An internal registry that mirrors them',
  manual: 'Loading and pushing them by hand',
};

// The one-time setup: how things reach the airgapped environment. The
// template itself comes from the otelgenie program.
export function Setup({ initial, cli, onDone, onCancel }: { initial: Profile; cli: CliTemplate; onDone: () => void; onCancel?: () => void }) {
  const [p, setP] = useState<Profile>(initial);
  const [step, setStep] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const set = (patch: Partial<Profile>) => setP({ ...p, ...patch });
  const toggle = (list: string[], v: string) => (list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);

  async function submit() {
    setBusy(true);
    setError('');
    try {
      const info = await call('describeTemplate', { snapshot: cli.snapshot });
      if (info.problems?.length) {
        throw new Error('The template has problems and cannot be used. Send this to its owner:\n' + info.problems.join('\n'));
      }
      await db.putSnapshot(info.snapshot);
      await db.set('profile', { ...p, template_repo: cli.source.url, template_ref: cli.source.ref });
      await db.set('commit', info.snapshot.commit);
      onDone();
    } catch (err: any) {
      // Whatever went wrong here is about the template, so show it there.
      setStep(0);
      setError(err.message ?? String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="space-y-4">
      <div>
        <h2 className="text-xl font-semibold">One-time setup</h2>
        <p className="mt-1 text-sm text-slate-600">
          These answers describe your environment, not a particular project. If a colleague has already done this, ask them for the values and
          compare fingerprints afterwards.
        </p>
      </div>

      <Wizard
        steps={STEPS}
        step={step}
        onStep={setStep}
        onFinish={submit}
        finish={busy ? 'Saving…' : 'Save'}
        busy={busy}
        onCancel={onCancel}
      >
        {step === 0 && (
          <Card title="The template">
            <Summary
              rows={[
                ['Kept at', cli.source.url],
                ['Version', cli.live ? 'This folder as it is now' : `${cli.source.ref || 'the default branch'}, at ${cli.source.commit.slice(0, 10)}`],
                ['Last fetched', cli.live ? 'Read anew every time this page loads' : new Date(cli.source.updated).toLocaleString()],
              ]}
            />
            <p className="mt-3 text-sm text-slate-600">
              The template comes from the otelgenie program on this machine, so there is nothing to choose here.
              {cli.live ? '' : ' To get the latest version, run the command below and reload this page.'}
            </p>
            {!cli.live && <Command where="In a terminal on this machine" text="otelgenie update" />}
          </Card>
        )}
        {step === 1 && (
          <Card title="How do things get into the airgapped environment?">
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Collector packages arrive by">
                <select className={inputClass} value={p.package_method} onChange={(e) => set({ package_method: e.target.value })}>
                  {Object.entries(PACKAGE_METHODS).map(([id, label]) => (
                    <option key={id} value={id}>
                      {label}
                    </option>
                  ))}
                </select>
              </Field>
              {p.package_method === 'repo' && (
                <Field label="Internal package repository address">
                  <input required className={inputClass} value={p.package_repo_url} onChange={(e) => set({ package_repo_url: e.target.value })} />
                </Field>
              )}
              <Field label="Container images arrive by" hint="Only matters for Kubernetes.">
                <select className={inputClass} value={p.image_method} onChange={(e) => set({ image_method: e.target.value })}>
                  {Object.entries(IMAGE_METHODS).map(([id, label]) => (
                    <option key={id} value={id}>
                      {label}
                    </option>
                  ))}
                </select>
              </Field>
              {p.image_method !== 'none' && (
                <Field label="Internal registry address">
                  <input required className={inputClass} value={p.registry} onChange={(e) => set({ registry: e.target.value })} />
                </Field>
              )}
              <Field
                label="Image with curl, for connectivity checks"
                hint="Only matters for Kubernetes. The check that a pod can reach the destination runs from this image; give one your clusters can pull, for example from your internal registry. Left empty, the commands use curlimages/curl."
              >
                <input
                  className={inputClass}
                  placeholder="quay.example.internal/tools/curl:latest"
                  value={p.curl_image ?? ''}
                  onChange={(e) => set({ curl_image: e.target.value })}
                />
              </Field>
              <Field
                label="Private certificate authority file"
                hint={p.ca_bundle ? 'A CA file is loaded. Choose another to replace it.' : 'Needed if the collector endpoint uses an internal certificate.'}
              >
                <input
                  type="file"
                  accept=".pem,.crt,.cer,text/plain"
                  className="block w-full text-sm"
                  onChange={async (e) => set({ ca_bundle: (await e.target.files?.[0]?.text()) ?? '' })}
                />
              </Field>
            </div>
          </Card>
        )}

        {step === 2 && (
          <>
            <Card title="What do you deploy to?">
              <div className="grid gap-2 sm:grid-cols-2">
                {OS_FAMILIES.map(([id, label]) => (
                  <Choice key={id} title={label} checked={p.os_families.includes(id)} onChange={() => set({ os_families: toggle(p.os_families, id) })} />
                ))}
              </div>
            </Card>
            <Card title="Check your answers">
              <Summary
                rows={[
                  ['Template', cli.live ? cli.source.url : `${cli.source.url} at ${cli.source.commit.slice(0, 10)}`],
                  ['Collector packages', p.package_method === 'repo' ? p.package_repo_url : PACKAGE_METHODS[p.package_method]],
                  ['Container images', p.image_method === 'none' ? IMAGE_METHODS.none : `${IMAGE_METHODS[p.image_method]} (${p.registry})`],
                  ['Image with curl', p.curl_image?.trim() || 'curlimages/curl (public)'],
                  ['Private certificate authority', p.ca_bundle ? 'Loaded' : 'None'],
                  ['Deploys to', OS_FAMILIES.filter(([id]) => p.os_families.includes(id)).map(([, label]) => label).join('; ') || 'Nothing chosen'],
                ]}
              />
            </Card>
          </>
        )}

        <ErrorNote message={error} />
      </Wizard>
    </div>
  );
}
