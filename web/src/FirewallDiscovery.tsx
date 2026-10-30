import { useEffect, useRef, useState } from 'react';
import { Api, messageOf } from './api';
import {
  accessListDiscoverySchema,
  subnetDiscoverySchema,
  type Firewall,
} from './schemas';
import type { FirewallDraft } from './settings-model';
import { Field, Notice } from './ui';
import { z } from 'zod';

type AccessLists = z.infer<typeof accessListDiscoverySchema>;
type Subnets = z.infer<typeof subnetDiscoverySchema>;

export function FirewallDiscovery({
  api,
  firewall,
  saved,
  revision,
  onSelect,
}: {
  api: Api;
  firewall: FirewallDraft;
  saved?: Firewall;
  revision: number;
  onSelect: (id: string) => void;
}) {
  const [lists, setLists] = useState<AccessLists | null>(null);
  const [subnets, setSubnets] = useState<Subnets | null>(null);
  const [listError, setListError] = useState('');
  const [subnetError, setSubnetError] = useState('');
  const [listBusy, setListBusy] = useState(false);
  const [subnetBusy, setSubnetBusy] = useState(false);
  const listRequest = useRef<AbortController | null>(null);
  const subnetRequest = useRef<AbortController | null>(null);
  const connectionSaved =
    !!saved &&
    firewall.id === saved.id &&
    firewall.baseUrl === saved.baseUrl &&
    (firewall.caPem ?? '') === (saved.caPem ?? '') &&
    !firewall.apiKey &&
    !firewall.apiSecret &&
    !firewall.clearApiKey &&
    !firewall.clearApiSecret;
  useEffect(() => {
    setLists(null);
    setSubnets(null);
    setListError('');
    setSubnetError('');
    setListBusy(false);
    setSubnetBusy(false);
    return () => {
      listRequest.current?.abort();
      subnetRequest.current?.abort();
    };
  }, [api, revision, firewall.id]);
  async function fetchLists() {
    if (!connectionSaved || listBusy) return;
    const controller = new AbortController();
    listRequest.current?.abort();
    listRequest.current = controller;
    setListBusy(true);
    setListError('');
    try {
      const result = await api.request(
        `/opnsenses/${encodeURIComponent(firewall.id)}/access-lists`,
        accessListDiscoverySchema,
        { signal: controller.signal },
      );
      if (result.opnsenseId !== firewall.id || result.revision !== revision)
        throw new Error(
          'Saved configuration changed. Reload its revision before fetching again.',
        );
      if (!controller.signal.aborted) setLists(result);
    } catch (err) {
      if (!controller.signal.aborted)
        setListError(messageOf(err instanceof Error ? err : null));
    } finally {
      if (!controller.signal.aborted) setListBusy(false);
    }
  }
  async function fetchSubnets() {
    if (!connectionSaved || subnetBusy) return;
    const controller = new AbortController();
    subnetRequest.current?.abort();
    subnetRequest.current = controller;
    setSubnetBusy(true);
    setSubnetError('');
    try {
      const result = await api.request(
        `/opnsenses/${encodeURIComponent(firewall.id)}/subnets`,
        subnetDiscoverySchema,
        { signal: controller.signal },
      );
      if (result.opnsenseId !== firewall.id || result.revision !== revision)
        throw new Error(
          'Saved configuration changed. Reload its revision before fetching again.',
        );
      if (!controller.signal.aborted) setSubnets(result);
    } catch (err) {
      if (!controller.signal.aborted)
        setSubnetError(messageOf(err instanceof Error ? err : null));
    } finally {
      if (!controller.signal.aborted) setSubnetBusy(false);
    }
  }
  const selected = lists?.accessLists.find(
    (a) => a.id === firewall.privateAccessListId,
  );
  return (
    <>
      <Field
        label="Default private access list"
        help="Use an existing Caddy list unchanged, or let the dashboard manage a separate list from your CIDRs. Selection affects reviewed private routes only."
      >
        {(id, helpId) => (
          <select
            id={id}
            aria-describedby={helpId}
            value={firewall.privateAccessListId ?? ''}
            onChange={(event) => onSelect(event.target.value)}
          >
            <option value="">Dashboard-managed CIDRs</option>
            {firewall.privateAccessListId && !selected && (
              <option value={firewall.privateAccessListId}>
                Selected UUID {firewall.privateAccessListId} (definition{' '}
                {lists ? 'missing' : 'not loaded'})
              </option>
            )}
            {lists?.accessLists.map((a) => (
              <option key={a.id} value={a.id} disabled={!a.suitable}>
                {a.name || a.id}
                {a.suitable ? '' : ` · Unavailable: ${a.reason}`}
              </option>
            ))}
          </select>
        )}
      </Field>
      <div className="actions">
        <button
          type="button"
          className="button"
          disabled={!connectionSaved || listBusy}
          onClick={() => {
            void fetchLists();
          }}
        >
          {listBusy
            ? 'Fetching Caddy access lists…'
            : 'Fetch saved Caddy access lists'}
        </button>
      </div>
      {!connectionSaved && (
        <p className="disabled-reason">
          Save this firewall’s connection and credential changes before
          discovery. Reads use saved credentials only.
        </p>
      )}
      {listError && (
        <Notice tone="danger" role="alert">
          Access list discovery failed. {listError}{' '}
          {lists
            ? 'Previously loaded definitions are stale.'
            : 'No definitions were loaded.'}{' '}
          The selected UUID is preserved.
        </Notice>
      )}
      {lists && (
        <div aria-label="Discovered Caddy access lists" aria-busy={listBusy}>
          <p className="muted">
            {listError || !connectionSaved ? 'Previously loaded' : 'Loaded'}{' '}
            from saved revision {lists.revision}. Plans verify current policy
            again.
          </p>
          {lists.accessLists.length === 0 ? (
            <p>
              No Caddy access lists exist on this firewall. Dashboard-managed
              CIDRs remain available.
            </p>
          ) : (
            <ul>
              {lists.accessLists.map((a) => (
                <li key={a.id}>
                  <strong>{a.name || a.id}</strong>:{' '}
                  {a.cidrs.join(', ') || 'No CIDRs'}. {a.description}{' '}
                  {!a.suitable && <span>Unavailable: {a.reason}</span>}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
      {firewall.privateAccessListId && (
        <p className="muted">
          Shared list {firewall.privateAccessListId}. Its policy stays
          externally managed and is excluded from dashboard cleanup. The manual
          CIDRs below do not govern this list.
        </p>
      )}
      <div className="actions">
        <button
          type="button"
          className="button"
          disabled={!connectionSaved || subnetBusy}
          onClick={() => {
            void fetchSubnets();
          }}
        >
          {subnetBusy
            ? 'Discovering KEA subnets…'
            : 'Discover saved KEA subnets'}
        </button>
      </div>
      <p className="muted">
        Each app’s intended IPv4 automatically selects exactly one configured
        KEA subnet. No subnet UUID is needed. Overlapping or missing matches
        block writes.
      </p>
      {subnetError && (
        <Notice tone="danger" role="alert">
          KEA discovery failed. {subnetError}{' '}
          {subnets && 'Previously loaded subnet information is stale.'}
        </Notice>
      )}
      {subnets && (
        <div aria-label="Discovered KEA subnets">
          <p className="muted">
            {subnetError || !connectionSaved ? 'Previously loaded' : 'Loaded'}{' '}
            from saved revision {subnets.revision}.
          </p>
          {subnets.subnets.length === 0 ? (
            <p>No KEA IPv4 subnets are configured.</p>
          ) : (
            <ul>
              {subnets.subnets.map((s) => (
                <li key={s.id}>
                  {s.cidr} {s.description}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </>
  );
}
