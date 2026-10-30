import { z } from 'zod';

// Go nil slices serialize as null. Accept null deliberately, but reject missing
// fields, wrong types and malformed records instead of inventing empty data.
const list = <T>(item: z.ZodType<T>) =>
  z
    .array(item)
    .nullable()
    .transform((value) => value ?? []);
const text = z.string();
const integer = z.number().int();
export const schemeSchema = z.enum(['http', 'https']);
export const routeSchema = z.object({
  scheme: schemeSchema,
  port: integer.min(1).max(65535),
  public: z.boolean(),
});
export const sshSchema = z.object({
  enabled: z.boolean(),
  host: text,
  port: integer,
  user: text,
  privateKeyFile: text,
  knownHostsFile: text,
});
export const hostSchema = z.object({
  addressProfiles: z
    .array(
      z.object({
        vmidMin: integer,
        vmidMax: integer,
        network: text,
        macPrefix: text,
      }),
    )
    .max(16)
    .optional(),
  id: text,
  name: text,
  baseUrl: text,
  node: text,
  opnsenseId: text,
  subnetId: text.optional(),
  vmidMin: integer,
  vmidMax: integer,
  network: text,
  macPrefix: text,
  tokenId: text,
  caPem: text.optional(),
  ssh: sshSchema,
});
export const hostViewSchema = hostSchema.extend({
  hasTokenSecret: z.boolean(),
});
export const firewallSchema = z.object({
  id: text,
  name: text,
  baseUrl: text,
  caPem: text.optional(),
  subnetId: text.optional(),
  privateAccessListId: z.union([z.uuid(), z.literal('')]).optional(),
  domainBase: text,
  allowlistCidrs: list(text),
  upstreamScheme: z.union([schemeSchema, z.literal('')]).optional(),
  upstreamPort: integer.optional(),
  certificateId: text,
  domainReady: z.boolean(),
});
export const firewallViewSchema = firewallSchema.extend({
  hasApiSecret: z.boolean(),
  hasApiKey: z.boolean(),
});
export const settingsSchema = z.object({
  revision: integer,
  hosts: list(hostViewSchema),
  opnsenses: list(firewallViewSchema),
});
export const profilesSchema = list(hostSchema);
export const containerSchema = z.object({
  key: text,
  hostId: text,
  node: text,
  vmid: integer,
  name: text,
  status: text,
  template: z.boolean(),
  ip: text,
  mac: text,
  expectedIp: text.optional(),
  expectedMac: text.optional(),
  distribution: text,
  version: text,
  distroSource: text,
  app: text,
  icon: text,
  identity: text,
  fingerprint: text,
  warnings: list(text),
  desiredRoute: routeSchema.nullish(),
});
export const hostInventorySchema = z.object({
  // The client retains each host's observation time across incomplete reads.
  observedAt: text.optional(),
  hostId: text,
  complete: z.boolean(),
  error: text,
  nextVmid: integer.nullable(),
  guests: list(z.object({ vmid: integer, type: text, node: text })),
});
export const inventorySchema = z.object({
  observedAt: text,
  complete: z.boolean(),
  containers: list(containerSchema),
  hosts: list(hostInventorySchema),
});
export const clientIPContextSchema = z.object({
  trustedProxyListId: z.union([z.uuid(), z.literal('')]),
  trustedProxyCidrs: z.array(text),
  headers: z.array(z.object({ id: z.uuid(), type: text })),
  headerOrder: text,
  enableLayer4: text,
  layer4EmptyVerified: z.boolean().optional(),
  proxyProtocolAllow: text,
  suitable: z.boolean(),
  reason: text,
  summary: text,
});
const accessListReferenceSchema = z.object({
  id: z.uuid(),
  fingerprint: text,
  clientIpContextFingerprint: text.optional(),
  clientIpContext: clientIPContextSchema.optional(),
});
export const accessListSchema = z.object({
  clientIpContext: clientIPContextSchema.optional(),
  id: z.uuid(),
  name: text,
  description: text,
  cidrs: z.array(text),
  matcher: text,
  inversion: text,
  suitable: z.boolean(),
  reason: text,
});
export const accessListDiscoverySchema = z.object({
  opnsenseId: text,
  revision: integer,
  accessLists: z.array(accessListSchema),
});
export const subnetReferenceSchema = z.object({
  id: z.uuid(),
  cidr: z.cidrv4(),
  fingerprint: text,
});
export const subnetDiscoverySchema = z.object({
  opnsenseId: text,
  revision: integer,
  subnets: z.array(
    z.object({ id: z.uuid(), cidr: z.cidrv4(), description: text }),
  ),
});
export const fieldChangeSchema = z.object({
  field: text,
  before: text,
  after: text,
});
const existingResourceSchema = z.object({
  kind: text,
  id: text,
  name: text,
  summary: text,
});
export const driftSchema = z.object({
  id: text,
  kind: text,
  status: z.enum(['missing', 'changed']),
  changes: z.array(fieldChangeSchema),
  detail: text,
});
export const maintenanceSchema = z.object({
  key: text,
  identity: text,
  fingerprint: text,
  observedAt: text,
  connected: z.boolean(),
  distribution: text,
  version: text,
  source: z.enum(['', 'ssh', 'demo']),
  appUpdate: z.boolean(),
  osUpdate: z.boolean(),
  reason: text,
  appReason: text,
  osReason: text,
});
export type MaintenanceReadiness = z.infer<typeof maintenanceSchema>;
export const itemSchema = z.object({
  drift: z.array(driftSchema).optional(),
  managedResources: z.array(existingResourceSchema).optional(),
  canAcceptChanges: z.boolean().optional(),
  acceptBlocked: text.optional(),
  importAvailable: z.boolean().optional(),
  importIssues: list(text).optional(),
  existingRoute: routeSchema.optional(),
  existingResources: list(
    z.object({
      kind: text,
      id: text,
      name: text,
      summary: text,
    }),
  ).optional(),
  routeUnavailable: z.boolean().optional(),
  key: text,
  state: z.enum([
    'missing',
    'healthy',
    'mismatch',
    'ownedOrphan',
    'conflict',
    'unknown',
  ]),
  issues: list(text),
  owned: z.boolean(),
  privateAccessList: accessListSchema.optional(),
  resolvedSubnet: subnetReferenceSchema.optional(),
  desiredRoute: routeSchema.nullish(),
});
const ownedResourceSchema = z.object({
  subnetId: text.optional(),
  id: text,
  kind: text,
  fingerprint: text,
});
const externalCleanupReviewSchema = z.object({
  foreignContextHash: text.optional(),
  address: z.object({ hostId: text, vmid: integer, ip: text, mac: text }),
  targets: list(ownedResourceSchema),
  contextHash: text,
  recoveryId: text.optional(),
  externalUseAcknowledged: z.literal(true),
});
export const operationSchema = z.object({
  externalCleanup: externalCleanupReviewSchema.optional(),
  cleanupTargets: list(ownedResourceSchema).optional(),
  resolvedSubnet: subnetReferenceSchema.optional(),
  sharedAccessList: accessListReferenceSchema.optional(),
  desiredRoute: routeSchema.optional(),
  id: text,
  planId: text,
  key: text,
  opnsenseId: text,
  action: text,
  status: z.enum(['running', 'succeeded', 'partial', 'interrupted']),
  step: text,
  error: text,
  manualRecovery: z.boolean(),
  createdAt: text,
  updatedAt: text,
  resources: list(ownedResourceSchema),
});
export const operationsSchema = list(operationSchema);
export const reconciliationSchema = z.object({
  protectedCandidates: z
    .array(
      z.object({
        opnsenseId: text,
        resource: existingResourceSchema,
        observations: z.array(fieldChangeSchema),
        reason: text,
        eligible: z.boolean().optional(),
        hostId: text.optional(),
        vmid: integer.optional(),
        fingerprint: text.optional(),
      }),
    )
    .optional(),
  inventory: inventorySchema,
  items: list(itemSchema),
  operations: operationsSchema,
});
export const planSchema = z
  .object({
    resolvedSubnet: subnetReferenceSchema.optional(),
    sharedAccessList: accessListReferenceSchema.optional(),
    id: text,
    action: z.enum([
      'ensure',
      'reservation',
      'cleanup',
      'externalCleanup',
      'import',
      'accept',
    ]),
    externalCleanup: externalCleanupReviewSchema.optional(),
    key: text,
    hostId: text,
    vmid: integer,
    opnsenseId: text,
    public: z.boolean(),
    desiredRoute: z.union([
      routeSchema,
      z.object({
        scheme: z.literal(''),
        port: z.literal(0),
        public: z.literal(false),
      }),
    ]),
    revision: integer,
    inventoryHash: text,
    providerHash: text,
    identity: text,
    createdAt: text,
    expiresAt: text,
    effects: list(
      z.object({
        kind: text,
        action: z.enum([
          'create',
          'update',
          'retain',
          'delete',
          'adopt',
          'acknowledge',
        ]),
        resourceId: text,
        name: text,
        changes: list(z.object({ field: text, before: text, after: text })),
      }),
    ),
    applyNotes: list(text),
  })
  .refine(
    (value) =>
      value.action === 'reservation' ||
      value.action === 'externalCleanup' ||
      routeSchema.safeParse(value.desiredRoute).success,
    {
      message:
        'Only reservation-only or external cleanup plans may have an unavailable app route.',
      path: ['desiredRoute'],
    },
  );
export const jobSchema = z.object({
  id: text,
  key: text,
  hostId: text,
  vmid: integer,
  kind: z.enum(['appUpdate', 'osUpdate']),
  fingerprint: text,
  status: z.enum([
    'queued',
    'running',
    'succeeded',
    'failed',
    'timedOut',
    'interrupted',
    'rebootRequired',
  ]),
  createdAt: text,
  updatedAt: text,
  error: text,
  acknowledged: z.boolean(),
  logs: list(text),
  truncated: z.boolean(),
});
export const jobsSchema = list(jobSchema);
export const jobLogsSchema = z.object({
  jobId: text,
  logs: list(text),
  truncated: z.boolean(),
});
export const providerSnapshotSchema = z.object({
  clientIpContext: clientIPContextSchema.optional(),
  resources: list(
    z.object({ id: text, kind: text, fields: z.record(text, text) }),
  ),
  subnet: text,
  ready: z.boolean(),
  issues: list(text),
});
export const authSchema = z.object({
  demo: z.boolean(),
  bearerEnabled: z.boolean(),
  oidcEnabled: z.boolean(),
  authenticated: z.boolean(),
  principal: text,
});
export const errorSchema = z.object({
  error: z.object({ code: text, message: text }),
});
export type Settings = z.infer<typeof settingsSchema>;
export type Host = z.infer<typeof hostViewSchema>;
export type Firewall = z.infer<typeof firewallViewSchema>;
export type Container = z.infer<typeof containerSchema>;
export type Inventory = z.infer<typeof inventorySchema>;
export type Reconciliation = z.infer<typeof reconciliationSchema>;
export type ReconcileItem = z.infer<typeof itemSchema>;
export type Plan = z.infer<typeof planSchema>;
export type Operation = z.infer<typeof operationSchema>;
export type Job = z.infer<typeof jobSchema>;
export type AuthStatus = z.infer<typeof authSchema>;
export type HostProfile = z.infer<typeof hostSchema>;
export type Scheme = z.infer<typeof schemeSchema>;
