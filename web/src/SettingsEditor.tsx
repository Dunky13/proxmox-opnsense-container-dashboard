import { useEffect, useRef, useState } from 'react';
import { useTheme } from 'next-themes';
import { CheckCircle, Plus, Trash } from '@phosphor-icons/react';
import { Api, ApiError, messageOf } from './api';
import {
  hostInventorySchema,
  profilesSchema,
  providerSnapshotSchema,
  settingsSchema,
  type HostProfile,
  type Settings,
} from './schemas';
import {
  createDraft,
  settingsPayload,
  validateDraft,
  type FirewallDraft,
  type HostDraft,
  type SettingsDraft,
} from './settings-model';
import { Badge, Field, Notice } from './ui';
import { FirewallDiscovery } from './FirewallDiscovery';

function SecretField({
  label,
  configured,
  value,
  clear,
  onValue,
  onClear,
}: {
  label: string;
  configured: boolean;
  value: string;
  clear: boolean;
  onValue: (value: string) => void;
  onClear: (clear: boolean) => void;
}) {
  return (
    <div className="secret-field">
      <Field
        label={label}
        help={
          configured
            ? 'Configured. Leave blank to keep the saved credential.'
            : 'Not configured. Enter a credential to set it.'
        }
      >
        {(id, helpId) => (
          <input
            id={id}
            aria-describedby={helpId}
            type="password"
            autoComplete="new-password"
            value={value}
            disabled={clear}
            onChange={(event) => onValue(event.target.value)}
          />
        )}
      </Field>
      <label className="check">
        <input
          type="checkbox"
          checked={clear}
          onChange={(event) => onClear(event.target.checked)}
        />
        Explicitly clear saved {label.toLowerCase()}
      </label>
    </div>
  );
}
function TextField({
  label,
  value,
  onChange,
  help,
  required = false,
  disabled = false,
  type = 'text',
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  help?: string;
  required?: boolean;
  disabled?: boolean;
  type?: string;
}) {
  return (
    <Field label={label} help={help}>
      {(id, helpId) => (
        <input
          id={id}
          type={type}
          aria-describedby={helpId}
          required={required}
          value={value}
          disabled={disabled}
          onChange={(event) => onChange(event.target.value)}
        />
      )}
    </Field>
  );
}
function NumberField({
  label,
  value,
  onChange,
  min = 1,
  max = 65535,
}: {
  label: string;
  value: number;
  onChange: (value: number) => void;
  min?: number;
  max?: number;
}) {
  return (
    <Field label={label}>
      {(id) => (
        <input
          id={id}
          type="number"
          required
          min={min}
          max={max}
          value={Number.isNaN(value) ? '' : value}
          onChange={(event) =>
            onChange(
              event.target.value === ''
                ? Number.NaN
                : Number(event.target.value),
            )
          }
        />
      )}
    </Field>
  );
}
export function SettingsEditor({
  api,
  settings,
  onSaved,
}: {
  api: Api;
  settings: Settings;
  onSaved: (settings: Settings) => Promise<void>;
}) {
  const [draft, setDraft] = useState(() => createDraft(settings));
  const [saved, setSaved] = useState(settings);
  const [profiles, setProfiles] = useState<HostProfile[]>([]);
  const [profileError, setProfileError] = useState('');
  const [error, setError] = useState('');
  const [validation, setValidation] = useState<string[]>([]);
  const [conflict, setConflict] = useState(false);
  const [busy, setBusy] = useState(false);
  const [testBusy, setTestBusy] = useState('');
  const [testResult, setTestResult] = useState('');
  const [success, setSuccess] = useState('');
  const [removing, setRemoving] = useState('');
  const [discarding, setDiscarding] = useState(false);
  const { theme, setTheme } = useTheme();
  const alive = useRef(true);
  const original = JSON.stringify(createDraft(saved));
  const dirty = JSON.stringify(draft) !== original;
  useEffect(() => {
    alive.current = true;
    const controller = new AbortController();
    api
      .request('/profiles', profilesSchema, { signal: controller.signal })
      .then((value) => {
        if (!controller.signal.aborted) setProfiles(value);
      })
      .catch((err) => {
        if (!controller.signal.aborted)
          setProfileError(messageOf(err instanceof Error ? err : null));
      });
    return () => {
      alive.current = false;
      controller.abort();
    };
  }, [api]);
  useEffect(() => {
    if (!dirty) return;
    const prevent = (event: BeforeUnloadEvent) => event.preventDefault();
    window.addEventListener('beforeunload', prevent);
    return () => window.removeEventListener('beforeunload', prevent);
  }, [dirty]);
  function change(update: (old: SettingsDraft) => SettingsDraft) {
    setDraft(update);
    setSuccess('');
    setTestResult('');
  }
  function hostChange(id: string, update: Partial<HostDraft>) {
    change((old) => ({
      ...old,
      hosts: old.hosts.map((h) =>
        h.editorKey === id ? { ...h, ...update } : h,
      ),
    }));
  }
  function firewallChange(id: string, update: Partial<FirewallDraft>) {
    change((old) => ({
      ...old,
      opnsenses: old.opnsenses.map((o) =>
        o.editorKey === id ? { ...o, ...update } : o,
      ),
    }));
  }
  function uniqueId(base: string, existing: string[]) {
    let id = base;
    let n = 2;
    while (existing.includes(id)) id = `${base}-${n++}`;
    return id;
  }
  function addHost(profile?: HostProfile) {
    const id = uniqueId(
      profile?.id || 'host',
      draft.hosts.map((h) => h.id),
    );
    const host: HostDraft = {
      editorKey: id,
      id,
      name: profile?.name || 'New Proxmox host',
      baseUrl: '',
      node: '',
      opnsenseId: draft.opnsenses[0]?.id || '',
      vmidMin: profile?.vmidMin ?? 601,
      vmidMax: profile?.vmidMax ?? 699,
      network: profile?.network ?? '192.168.6.0/24',
      macPrefix: profile?.macPrefix ?? 'd0:cc:37:00:06',
      tokenId: '',
      tokenSecret: '',
      clearTokenSecret: false,
      hasTokenSecret: false,
      ssh: {
        enabled: false,
        host: '',
        port: 22,
        user: 'root',
        privateKeyFile: '',
        knownHostsFile: '',
      },
    };
    change((old) => ({ ...old, hosts: [...old.hosts, host] }));
  }
  function addFirewall() {
    const firewallId = uniqueId(
      'opnsense',
      draft.opnsenses.map((o) => o.id),
    );
    const firewall: FirewallDraft = {
      editorKey: firewallId,
      id: firewallId,
      name: 'New OPNsense',
      baseUrl: '',
      subnetId: '',
      domainBase: 'went.io',
      allowlistCidrs: [],
      certificateId: '',
      domainReady: false,
      apiKey: '',
      apiSecret: '',
      hasApiKey: false,
      hasApiSecret: false,
      clearApiKey: false,
      clearApiSecret: false,
    };
    change((old) => ({ ...old, opnsenses: [...old.opnsenses, firewall] }));
  }
  async function save() {
    setValidation([]);
    setError('');
    setSuccess('');
    setConflict(false);
    const errors = validateDraft(draft);
    if (errors.length) {
      setValidation(errors);
      return;
    }
    setBusy(true);
    try {
      const result = await api.request('/settings', settingsSchema, {
        method: 'PUT',
        body: JSON.stringify(settingsPayload(draft)),
      });
      if (alive.current) {
        setSaved(result);
        setDraft(createDraft(result));
        setSuccess(
          'Configuration saved. Connection tests now use this revision.',
        );
        setTestResult('');
      }
      if (alive.current) await onSaved(result);
    } catch (err) {
      if (alive.current) {
        setError(messageOf(err instanceof Error ? err : null));
        setConflict(err instanceof ApiError && err.status === 409);
      }
    } finally {
      if (alive.current) setBusy(false);
    }
  }
  async function reload() {
    setBusy(true);
    setError('');
    try {
      const result = await api.request('/settings', settingsSchema);
      if (alive.current) {
        setSaved(result);
        setDraft(createDraft(result));
        setConflict(false);
        setDiscarding(false);
        setValidation([]);
        setSuccess('Saved revision loaded. Local edits were discarded.');
        setTestResult('');
      }
      if (alive.current) await onSaved(result);
    } catch (err) {
      if (alive.current) setError(messageOf(err instanceof Error ? err : null));
    } finally {
      if (alive.current) setBusy(false);
    }
  }
  async function test(kind: 'hosts' | 'opnsenses', id: string) {
    setTestBusy(`${kind}:${id}`);
    setError('');
    setTestResult('');
    try {
      let result: string;
      if (kind === 'hosts') {
        const inventory = await api.request(
          `/hosts/${encodeURIComponent(id)}/test`,
          hostInventorySchema,
          { method: 'POST', body: '{}' },
        );
        result = inventory.complete
          ? `${id}: Proxmox inventory read succeeded. Next VMID ${inventory.nextVmid ?? 'range full'}.`
          : `${id}: inventory is incomplete. ${inventory.error || 'Inspect host configuration and credentials.'}`;
      } else {
        const snapshot = await api.request(
          `/opnsenses/${encodeURIComponent(id)}/test`,
          providerSnapshotSchema,
          { method: 'POST', body: '{}' },
        );
        result = `${id}: ${snapshot.ready ? 'KEA and Caddy read preflight passed.' : 'Preflight has missing prerequisites.'} ${snapshot.issues.join(' ')}`;
      }
      if (alive.current) setTestResult(result);
    } catch (err) {
      if (alive.current) setError(messageOf(err instanceof Error ? err : null));
    } finally {
      if (alive.current) setTestBusy('');
    }
  }
  return (
    <div className="settings-page">
      <div className="page-heading">
        <div>
          <h1>Settings</h1>
          <p>Proxmox inventory, OPNsense routing and server-side access.</p>
        </div>
        <Badge>Saved revision {saved.revision}</Badge>
      </div>
      <section className="settings-section appearance">
        <div>
          <h2>Appearance</h2>
          <p className="muted">
            Follow your system or choose a theme for this browser.
          </p>
        </div>
        <Field label="Color theme">
          {(id) => (
            <select
              id={id}
              value={theme ?? 'system'}
              onChange={(event) => setTheme(event.target.value)}
            >
              <option value="system">System</option>
              <option value="light">Light</option>
              <option value="dark">Dark</option>
            </select>
          )}
        </Field>
      </section>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          void save();
        }}
      >
        {settings.revision !== saved.revision && (
          <Notice>
            The server is now at revision {settings.revision}. This draft is
            based on revision {saved.revision}. Reload the saved revision when
            ready to discard local edits.
          </Notice>
        )}
        <fieldset disabled={busy} className="settings-fieldset">
          <section className="settings-section">
            <div className="section-heading">
              <div>
                <h2>OPNsense firewalls</h2>
                <p className="muted">
                  Several Proxmox hosts can use the same firewall.
                </p>
              </div>
              <button type="button" className="button" onClick={addFirewall}>
                <Plus size={16} />
                Add OPNsense
              </button>
            </div>
            {draft.opnsenses.length === 0 && (
              <p>
                No firewalls configured. Add OPNsense, then link a Proxmox host.
              </p>
            )}
            {draft.opnsenses.map((o, index) => (
              <details
                className="provider-editor"
                aria-label={`Edit OPNsense ${o.name}`}
                key={o.editorKey}
                open={
                  draft.opnsenses.length === 1 ||
                  !saved.opnsenses.some((item) => item.id === o.id)
                }
              >
                <summary>
                  <strong>{o.name || `OPNsense ${index + 1}`}</strong>
                  <span className="muted">
                    {o.id} ·{' '}
                    {draft.hosts.filter((h) => h.opnsenseId === o.id).length}{' '}
                    linked hosts
                  </span>
                </summary>
                <div className="provider-fields">
                  <div className="form-grid two">
                    <TextField
                      label="Configuration ID"
                      value={o.id}
                      disabled={saved.opnsenses.some(
                        (item) => item.id === o.id,
                      )}
                      required
                      onChange={(value) => {
                        firewallChange(o.editorKey, { id: value });
                        change((old) => ({
                          ...old,
                          hosts: old.hosts.map((h) =>
                            h.opnsenseId === o.id
                              ? { ...h, opnsenseId: value }
                              : h,
                          ),
                        }));
                      }}
                    />
                    <TextField
                      label="Display name"
                      required
                      value={o.name}
                      onChange={(name) => firewallChange(o.editorKey, { name })}
                    />
                    <TextField
                      label="OPNsense HTTPS URL"
                      required
                      type="url"
                      value={o.baseUrl}
                      onChange={(baseUrl) =>
                        firewallChange(o.editorKey, { baseUrl })
                      }
                    />
                    <SecretField
                      label="API key"
                      configured={o.hasApiKey}
                      value={o.apiKey}
                      clear={o.clearApiKey}
                      onValue={(apiKey) =>
                        firewallChange(o.editorKey, { apiKey })
                      }
                      onClear={(clearApiKey) =>
                        firewallChange(o.editorKey, { clearApiKey })
                      }
                    />
                    <SecretField
                      label="API secret"
                      configured={o.hasApiSecret}
                      value={o.apiSecret}
                      clear={o.clearApiSecret}
                      onValue={(apiSecret) =>
                        firewallChange(o.editorKey, { apiSecret })
                      }
                      onClear={(clearApiSecret) =>
                        firewallChange(o.editorKey, { clearApiSecret })
                      }
                    />
                    <TextField
                      label="Domain base"
                      value={o.domainBase}
                      required
                      onChange={(domainBase) =>
                        firewallChange(o.editorKey, { domainBase })
                      }
                    />
                  </div>
                  <FirewallDiscovery
                    api={api}
                    firewall={o}
                    saved={saved.opnsenses.find((item) => item.id === o.id)}
                    revision={saved.revision}
                    onSelect={(privateAccessListId) =>
                      firewallChange(o.editorKey, { privateAccessListId })
                    }
                  />
                  <Field
                    label="Private route allowlist CIDRs"
                    help="Used only in Dashboard-managed CIDRs mode. One restricted canonical CIDR per line, including each intended LAN, VPN and IPv6 range. An empty list blocks that mode."
                  >
                    {(id, helpId) => (
                      <textarea
                        id={id}
                        aria-describedby={helpId}
                        rows={4}
                        value={o.allowlistCidrs.join('\n')}
                        onChange={(event) =>
                          firewallChange(o.editorKey, {
                            allowlistCidrs: event.target.value.split('\n'),
                          })
                        }
                      />
                    )}
                  </Field>
                  <details>
                    <summary>Optional custom certificate override</summary>
                    <TextField
                      label="Custom certificate reference (optional)"
                      value={o.certificateId}
                      onChange={(certificateId) =>
                        firewallChange(o.editorKey, { certificateId })
                      }
                      help="Leave blank for os-caddy automatic HTTPS and its existing global TLS configuration. Existing explicit overrides are preserved; this dashboard does not configure TLS globally."
                    />
                  </details>
                  <Field
                    label="Trusted CA PEM (optional)"
                    help="Additional CA certificate for verified TLS. Leave blank to use system trust."
                  >
                    {(id, helpId) => (
                      <textarea
                        id={id}
                        aria-describedby={helpId}
                        rows={3}
                        value={o.caPem ?? ''}
                        onChange={(event) =>
                          firewallChange(o.editorKey, {
                            caPem: event.target.value,
                          })
                        }
                      />
                    )}
                  </Field>
                  <div className="actions">
                    <button
                      type="button"
                      className="button"
                      disabled={
                        dirty ||
                        !!testBusy ||
                        !saved.opnsenses.some((item) => item.id === o.id)
                      }
                      onClick={() => {
                        void test('opnsenses', o.id);
                      }}
                    >
                      {testBusy === `opnsenses:${o.id}`
                        ? 'Testing saved firewall…'
                        : 'Test saved OPNsense'}
                    </button>
                    <button
                      type="button"
                      className="button danger-text"
                      disabled={draft.hosts.some((h) => h.opnsenseId === o.id)}
                      onClick={() => setRemoving(`firewall:${o.id}`)}
                    >
                      <Trash size={16} />
                      Remove firewall
                    </button>
                  </div>
                  {draft.hosts.some((h) => h.opnsenseId === o.id) && (
                    <small>
                      Relink or remove this firewall’s hosts before removing it.
                    </small>
                  )}
                  {removing === `firewall:${o.id}` && (
                    <div className="inline-review">
                      <p>
                        Remove {o.name} from the draft? Save configuration to
                        request removal. The server refuses removal while owned
                        resources or incomplete operations exist.
                      </p>
                      <div className="actions">
                        <button
                          type="button"
                          className="button danger-button"
                          onClick={() => {
                            change((old) => ({
                              ...old,
                              opnsenses: old.opnsenses.filter(
                                (item) => item.id !== o.id,
                              ),
                            }));
                            setRemoving('');
                          }}
                        >
                          Remove from draft
                        </button>
                        <button
                          type="button"
                          className="button"
                          onClick={() => setRemoving('')}
                        >
                          Keep firewall
                        </button>
                      </div>
                    </div>
                  )}
                </div>
              </details>
            ))}
          </section>
          <section className="settings-section">
            <div className="section-heading">
              <div>
                <h2>Proxmox hosts</h2>
                <p className="muted">
                  Each host maps to one OPNsense firewall and an address
                  profile.
                </p>
              </div>
              <button
                type="button"
                className="button"
                onClick={() => addHost()}
              >
                <Plus size={16} />
                Add custom host
              </button>
            </div>
            <div className="actions">
              {profiles.map((profile) => (
                <button
                  key={profile.id}
                  className="button"
                  type="button"
                  onClick={() => addHost(profile)}
                >
                  <Plus size={16} />
                  Use {profile.name} profile{' '}
                  <span className="muted">
                    {profile.vmidMin}–{profile.vmidMax}
                  </span>
                </button>
              ))}
            </div>
            {profileError && (
              <Notice tone="danger">
                Host profiles could not be loaded. {profileError}
              </Notice>
            )}
            {draft.hosts.length === 0 && (
              <p>
                No hosts configured. Start with Main or pvedell, or add a custom
                host.
              </p>
            )}
            {draft.hosts.map((h, index) => (
              <details
                className="provider-editor"
                aria-label={`Edit Proxmox host ${h.name}`}
                key={h.editorKey}
                open={
                  draft.hosts.length === 1 ||
                  !saved.hosts.some((item) => item.id === h.id)
                }
              >
                <summary>
                  <strong>{h.name || `Host ${index + 1}`}</strong>
                  <span className="muted">
                    {h.id} · VMID {h.vmidMin}–{h.vmidMax}
                  </span>
                </summary>
                <div className="provider-fields">
                  <div className="form-grid two">
                    <TextField
                      label="Host ID"
                      required
                      value={h.id}
                      disabled={saved.hosts.some((item) => item.id === h.id)}
                      onChange={(id) => hostChange(h.editorKey, { id })}
                    />
                    <TextField
                      label="Display name"
                      value={h.name}
                      required
                      onChange={(name) => hostChange(h.editorKey, { name })}
                    />
                    <TextField
                      label="Proxmox HTTPS URL"
                      type="url"
                      required
                      value={h.baseUrl}
                      onChange={(baseUrl) =>
                        hostChange(h.editorKey, { baseUrl })
                      }
                    />
                    <TextField
                      label="Proxmox node"
                      required
                      value={h.node}
                      onChange={(node) => hostChange(h.editorKey, { node })}
                    />
                    <Field label="Linked OPNsense">
                      {(id) => (
                        <select
                          id={id}
                          required
                          value={h.opnsenseId}
                          onChange={(event) =>
                            hostChange(h.editorKey, {
                              opnsenseId: event.target.value,
                            })
                          }
                        >
                          <option value="">Choose firewall</option>
                          {draft.opnsenses.map((o) => (
                            <option key={o.id} value={o.id}>
                              {o.name} ({o.id})
                            </option>
                          ))}
                        </select>
                      )}
                    </Field>
                    <NumberField
                      label="First VMID"
                      max={9999}
                      min={100}
                      value={h.vmidMin}
                      onChange={(vmidMin) =>
                        hostChange(h.editorKey, { vmidMin })
                      }
                    />
                    <NumberField
                      label="Last VMID"
                      max={9999}
                      min={100}
                      value={h.vmidMax}
                      onChange={(vmidMax) =>
                        hostChange(h.editorKey, { vmidMax })
                      }
                    />
                    <TextField
                      label="IPv4 network /24"
                      required
                      value={h.network}
                      onChange={(network) =>
                        hostChange(h.editorKey, { network })
                      }
                    />
                    <TextField
                      label="MAC prefix (five bytes)"
                      required
                      value={h.macPrefix}
                      onChange={(macPrefix) =>
                        hostChange(h.editorKey, { macPrefix })
                      }
                    />
                    <TextField
                      label="Proxmox API token ID"
                      value={h.tokenId}
                      onChange={(tokenId) =>
                        hostChange(h.editorKey, { tokenId })
                      }
                    />
                    <SecretField
                      label="Token secret"
                      configured={h.hasTokenSecret}
                      value={h.tokenSecret}
                      clear={h.clearTokenSecret}
                      onValue={(tokenSecret) =>
                        hostChange(h.editorKey, { tokenSecret })
                      }
                      onClear={(clearTokenSecret) =>
                        hostChange(h.editorKey, { clearTokenSecret })
                      }
                    />
                  </div>
                  <details>
                    <summary>Additional address profiles for this host</summary>
                    <p className="muted">
                      Use another VMID/network profile when this Proxmox host
                      spans multiple VLANs. These describe intended container
                      addresses, independently of discovered KEA subnet sizes.
                    </p>
                    {(h.addressProfiles ?? []).map((profile, profileIndex) => {
                      const update = (values: Partial<typeof profile>) =>
                        hostChange(h.editorKey, {
                          addressProfiles: (h.addressProfiles ?? []).map(
                            (item, index) =>
                              index === profileIndex
                                ? { ...item, ...values }
                                : item,
                          ),
                        });
                      return (
                        <div
                          key={profileIndex}
                          className="form-grid two"
                          role="group"
                          aria-label={`Additional address profile ${profileIndex + 1}`}
                        >
                          <NumberField
                            label={`Profile ${profileIndex + 1} first VMID`}
                            value={profile.vmidMin}
                            min={100}
                            max={9999}
                            onChange={(vmidMin) => update({ vmidMin })}
                          />
                          <NumberField
                            label={`Profile ${profileIndex + 1} last VMID`}
                            value={profile.vmidMax}
                            min={100}
                            max={9999}
                            onChange={(vmidMax) => update({ vmidMax })}
                          />
                          <TextField
                            label={`Profile ${profileIndex + 1} IPv4 network /24`}
                            value={profile.network}
                            onChange={(network) => update({ network })}
                          />
                          <TextField
                            label={`Profile ${profileIndex + 1} MAC prefix`}
                            value={profile.macPrefix}
                            onChange={(macPrefix) => update({ macPrefix })}
                          />
                          <button
                            className="button danger-text"
                            type="button"
                            onClick={() =>
                              hostChange(h.editorKey, {
                                addressProfiles: (
                                  h.addressProfiles ?? []
                                ).filter((_, index) => index !== profileIndex),
                              })
                            }
                          >
                            Remove address profile {profileIndex + 1}
                          </button>
                        </div>
                      );
                    })}
                    <button
                      className="button"
                      type="button"
                      disabled={(h.addressProfiles?.length ?? 0) >= 16}
                      onClick={() =>
                        hostChange(h.editorKey, {
                          addressProfiles: [
                            ...(h.addressProfiles ?? []),
                            {
                              vmidMin: 701,
                              vmidMax: 799,
                              network: '192.168.7.0/24',
                              macPrefix: 'd0:cc:37:00:07',
                            },
                          ],
                        })
                      }
                    >
                      Add address profile
                    </button>
                  </details>
                  <p className="muted">
                    Decimal suffix mapping: VMID 612 becomes IP 192.168.6.12 and
                    MAC d0:cc:37:00:06:12. Allocation includes all cluster LXC
                    and QEMU guests.
                  </p>
                  <Field label="Trusted CA PEM (optional)">
                    {(id) => (
                      <textarea
                        id={id}
                        rows={3}
                        value={h.caPem ?? ''}
                        onChange={(event) =>
                          hostChange(h.editorKey, { caPem: event.target.value })
                        }
                      />
                    )}
                  </Field>
                  <details className="detail-section">
                    <summary>Maintenance SSH setup</summary>
                    <p>
                      Install the reviewed deploy/container-proxy-ssh wrapper on
                      this Proxmox host, owned by root. Create a dedicated key
                      for this host on the dashboard server and restrict its
                      authorized key with restrict, a source address and the
                      forced command. Pin the host key using a trusted console.
                      Allow only dashboard-to-host TCP 22, then save the SSH
                      settings below.
                    </p>
                    <p>
                      Never copy a personal root key or enable unrestricted root
                      SSH. Provider API access alone does not grant maintenance
                      access. After saving, open a running container and choose
                      Check maintenance readiness. It verifies the connection,
                      actual distro and executable /usr/bin/update. See the
                      operator procedure in docs/container-maintenance.md.
                    </p>
                  </details>
                  <label className="check">
                    <input
                      type="checkbox"
                      checked={h.ssh.enabled}
                      onChange={(event) =>
                        hostChange(h.editorKey, {
                          ssh: { ...h.ssh, enabled: event.target.checked },
                        })
                      }
                    />
                    Enable server-side SSH for inspection and explicit
                    maintenance
                  </label>
                  {h.ssh.enabled && (
                    <div className="ssh-fields">
                      <p className="muted">
                        Key files live on the dashboard server. Host key
                        verification is mandatory. Nothing here uploads or
                        generates a key.
                      </p>
                      <div className="form-grid two">
                        <TextField
                          label="SSH host"
                          required
                          value={h.ssh.host}
                          onChange={(host) =>
                            hostChange(h.editorKey, { ssh: { ...h.ssh, host } })
                          }
                        />
                        <NumberField
                          label="SSH port"
                          value={h.ssh.port}
                          onChange={(port) =>
                            hostChange(h.editorKey, { ssh: { ...h.ssh, port } })
                          }
                        />
                        <TextField
                          label="SSH user"
                          required
                          value={h.ssh.user}
                          onChange={(user) =>
                            hostChange(h.editorKey, { ssh: { ...h.ssh, user } })
                          }
                        />
                        <TextField
                          label="Private key file on server"
                          required
                          value={h.ssh.privateKeyFile}
                          onChange={(privateKeyFile) =>
                            hostChange(h.editorKey, {
                              ssh: { ...h.ssh, privateKeyFile },
                            })
                          }
                        />
                        <TextField
                          label="Known-hosts file on server"
                          required
                          value={h.ssh.knownHostsFile}
                          onChange={(knownHostsFile) =>
                            hostChange(h.editorKey, {
                              ssh: { ...h.ssh, knownHostsFile },
                            })
                          }
                        />
                      </div>
                    </div>
                  )}
                  <div className="actions">
                    <button
                      type="button"
                      className="button"
                      disabled={
                        dirty ||
                        !!testBusy ||
                        !saved.hosts.some((item) => item.id === h.id)
                      }
                      onClick={() => {
                        void test('hosts', h.id);
                      }}
                    >
                      {testBusy === `hosts:${h.id}`
                        ? 'Testing saved host…'
                        : 'Test saved Proxmox'}
                    </button>
                    <button
                      type="button"
                      className="button danger-text"
                      onClick={() => setRemoving(`host:${h.id}`)}
                    >
                      <Trash size={16} />
                      Remove host
                    </button>
                  </div>
                  {removing === `host:${h.id}` && (
                    <div className="inline-review">
                      <p>
                        Remove {h.name} from the draft? Save configuration to
                        request removal. The server protects enrolled resources
                        and unfinished operations.
                      </p>
                      <div className="actions">
                        <button
                          type="button"
                          className="button danger-button"
                          onClick={() => {
                            change((old) => ({
                              ...old,
                              hosts: old.hosts.filter(
                                (item) => item.id !== h.id,
                              ),
                            }));
                            setRemoving('');
                          }}
                        >
                          Remove from draft
                        </button>
                        <button
                          type="button"
                          className="button"
                          onClick={() => setRemoving('')}
                        >
                          Keep host
                        </button>
                      </div>
                    </div>
                  )}
                </div>
              </details>
            ))}
          </section>
        </fieldset>
        {dirty && (
          <p className="muted">
            Unsaved changes. Save before testing. Tests read the saved server
            revision, never this draft. Navigation within the dashboard
            preserves this draft.
          </p>
        )}
        {validation.length > 0 && (
          <Notice tone="danger" role="alert">
            <strong>Check configuration fields</strong>
            <ul>
              {validation.map((item) => (
                <li key={item}>{item}</li>
              ))}
            </ul>
          </Notice>
        )}
        {error && (
          <Notice tone="danger" role="alert">
            {error}
            {conflict && (
              <p>
                The draft is preserved. Another revision or enrolled resources
                may block this change. Reload the saved revision only when ready
                to discard local edits.
              </p>
            )}
          </Notice>
        )}
        {testResult && (
          <Notice tone="neutral" role="status">
            {testResult}
          </Notice>
        )}
        {success && (
          <p className="save-success" role="status">
            <CheckCircle size={18} />
            {success}
          </p>
        )}
        <div className="settings-save">
          <button
            className="button primary"
            type="submit"
            disabled={!dirty || busy || !!testBusy}
          >
            {busy ? 'Saving and refreshing…' : 'Save configuration'}
          </button>
          <button
            className="button"
            type="button"
            disabled={busy}
            onClick={() => setDiscarding(true)}
          >
            Reload saved revision
          </button>
          {dirty && <Badge tone="warning">Unsaved changes</Badge>}
        </div>
        {discarding && (
          <div className="inline-review">
            <p>
              Load the latest saved revision? This discards all unsaved fields,
              including entered credentials.
            </p>
            <div className="actions">
              <button
                type="button"
                className="button danger-button"
                disabled={busy}
                onClick={() => {
                  void reload();
                }}
              >
                Discard draft and reload
              </button>
              <button
                type="button"
                className="button"
                onClick={() => setDiscarding(false)}
              >
                Keep editing
              </button>
            </div>
          </div>
        )}
      </form>
    </div>
  );
}
