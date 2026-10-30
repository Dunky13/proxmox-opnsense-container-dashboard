import { z } from 'zod';
import {
  firewallSchema,
  hostSchema,
  type Firewall,
  type Host,
  type Settings,
} from './schemas';

export type HostDraft = Host & {
  editorKey: string;
  tokenSecret: string;
  clearTokenSecret: boolean;
};
export type FirewallDraft = Firewall & {
  editorKey: string;
  apiKey: string;
  apiSecret: string;
  clearApiKey: boolean;
  clearApiSecret: boolean;
};
export type SettingsDraft = {
  revision: number;
  hosts: HostDraft[];
  opnsenses: FirewallDraft[];
};
export function createDraft(settings: Settings): SettingsDraft {
  return {
    revision: settings.revision,
    hosts: settings.hosts.map((host) => ({
      ...host,
      editorKey: host.id,
      tokenSecret: '',
      clearTokenSecret: false,
    })),
    opnsenses: settings.opnsenses.map((firewall) => ({
      ...firewall,
      editorKey: firewall.id,
      apiKey: '',
      apiSecret: '',
      clearApiKey: false,
      clearApiSecret: false,
    })),
  };
}
export function settingsPayload(draft: SettingsDraft) {
  return {
    revision: draft.revision,
    hosts: draft.hosts.map((host) => ({
      ...hostSchema.parse(host),
      ...(host.clearTokenSecret
        ? { tokenSecret: '' }
        : host.tokenSecret
          ? { tokenSecret: host.tokenSecret }
          : {}),
    })),
    opnsenses: draft.opnsenses.map((firewall) => ({
      ...firewallSchema.parse({
        ...firewall,
        allowlistCidrs: firewall.allowlistCidrs
          .map((value) => value.trim())
          .filter(Boolean),
      }),
      ...(firewall.clearApiKey
        ? { apiKey: '' }
        : firewall.apiKey
          ? { apiKey: firewall.apiKey }
          : {}),
      ...(firewall.clearApiSecret
        ? { apiSecret: '' }
        : firewall.apiSecret
          ? { apiSecret: firewall.apiSecret }
          : {}),
    })),
  };
}
const slug = z.string().regex(/^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$/);
const https = z.url().refine((value) => {
  const url = new URL(value);
  return (
    url.protocol === 'https:' &&
    !url.username &&
    !url.password &&
    !url.search &&
    !url.hash &&
    (url.pathname === '/' || url.pathname === '')
  );
});
const cidr = z
  .union([z.cidrv4(), z.cidrv6()])
  .refine((value) => !value.endsWith('/0'));
const validHost = hostSchema.extend({
  id: slug,
  name: z.string().min(1),
  baseUrl: https,
  node: slug,
  opnsenseId: slug,
  vmidMin: z.number().int().min(100),
  vmidMax: z.number().int().max(9999),
  network: z.cidrv4(),
  macPrefix: z.string().regex(/^(?:[0-9a-fA-F]{2}:){4}[0-9a-fA-F]{2}$/),
  subnetId: z.union([z.uuid(), z.literal('')]).optional(),
});
const validFirewall = firewallSchema.extend({
  id: slug,
  name: z.string().min(1),
  baseUrl: https,
  subnetId: z.union([z.uuid(), z.literal('')]).optional(),
  domainBase: z.hostname().refine((value) => value.includes('.')),
  allowlistCidrs: z.array(cidr).max(64),
});
export function validateDraft(draft: SettingsDraft): string[] {
  const result = z
    .object({
      revision: z.number().int(),
      hosts: z.array(validHost).max(100),
      opnsenses: z.array(validFirewall).max(50),
    })
    .safeParse({
      ...draft,
      opnsenses: draft.opnsenses.map((o) => ({
        ...o,
        allowlistCidrs: o.allowlistCidrs
          .map((value) => value.trim())
          .filter(Boolean),
      })),
    });
  if (!result.success)
    return result.error.issues.map(
      (issue) => `${issue.path.join(' › ')}: ${issue.message}`,
    );
  const errors: string[] = [];
  if (new Set(draft.hosts.map((h) => h.id)).size !== draft.hosts.length)
    errors.push('Proxmox host IDs must be unique.');
  if (new Set(draft.opnsenses.map((o) => o.id)).size !== draft.opnsenses.length)
    errors.push('OPNsense IDs must be unique.');
  for (const h of draft.hosts) {
    if (!draft.opnsenses.some((o) => o.id === h.opnsenseId))
      errors.push(`${h.name}: select a configured OPNsense.`);
    const profiles = [h, ...(h.addressProfiles ?? [])];
    for (const [index, profile] of profiles.entries()) {
      const block = Math.floor(profile.vmidMin / 100);
      const networkPart = profile.network.split('/');
      const octets = networkPart[0]?.split('.') ?? [];
      if (
        profile.vmidMin < 100 ||
        profile.vmidMax > 9999 ||
        profile.vmidMax < profile.vmidMin ||
        Math.floor(profile.vmidMax / 100) !== block ||
        profile.vmidMin % 100 === 0 ||
        networkPart[1] !== '24' ||
        Number(octets[2]) !== block ||
        octets[3] !== '0' ||
        !/^(?:[0-9a-fA-F]{2}:){4}[0-9a-fA-F]{2}$/.test(profile.macPrefix) ||
        profile.macPrefix.split(':')[4] !== String(block).padStart(2, '0')
      )
        errors.push(
          `${h.name}, address profile ${index + 1}: VMIDs must share one hundred block matching the network /24 and final MAC prefix byte. Use suffixes 01–99.`,
        );
      if (
        profiles
          .slice(0, index)
          .some(
            (prior) =>
              profile.vmidMin <= prior.vmidMax &&
              profile.vmidMax >= prior.vmidMin,
          )
      )
        errors.push(`${h.name}: address profile VMID ranges overlap.`);
    }
    if (
      h.ssh.enabled &&
      (!h.ssh.host ||
        !h.ssh.user ||
        !h.ssh.privateKeyFile ||
        !h.ssh.knownHostsFile ||
        h.ssh.port < 1 ||
        h.ssh.port > 65535)
    )
      errors.push(
        `${h.name}: SSH requires host, port, user, server-side private key and known-hosts file.`,
      );
    if (/[\r\n]/.test(h.tokenId + h.tokenSecret))
      errors.push(`${h.name}: token credentials cannot contain line breaks.`);
  }
  return errors;
}
