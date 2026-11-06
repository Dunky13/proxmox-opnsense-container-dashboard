import { describe, expect, it } from 'vitest';
import {
  cleanupEligibility,
  currentReadiness,
  eligibility,
  reasonOf,
  type ActionId,
} from '../src/eligibility';
import type {
  Job,
  MaintenanceReadiness,
  Reconciliation,
  ReconcileItem,
} from '../src/schemas';
import {
  containerViews,
  type ContainerView,
  type JobAttemptState,
} from '../src/viewmodel';
import {
  container,
  job,
  partial,
  reconciliation,
  settings,
  withIssues,
} from './fixtures';

type Setup = {
  view: ContainerView;
  data: Reconciliation;
  jobs: Job[];
  activityLoaded: boolean;
  attempt?: JobAttemptState;
  readiness?: MaintenanceReadiness | null;
  now: number;
};

const route = { scheme: 'http', port: 8080, public: false } as const;
const importable: ReconcileItem = {
  key: container.key,
  state: 'conflict',
  owned: false,
  importAvailable: true,
  importIssues: [],
  existingRoute: route,
  existingResources: [
    { kind: 'reservation', id: 'res-1', name: 'immich', summary: 'exact' },
  ],
  ...withIssues('unownedConflict', ['unowned reservation matches']),
};
const orphan: ReconcileItem = {
  key: 'main:700',
  state: 'ownedOrphan',
  owned: true,
  ...withIssues('ownedOrphan', ['no matching VMID']),
};

function viewWith(
  setup: Setup,
  patch: Partial<ContainerView['container']> = {},
  item?: ReconcileItem,
) {
  const base = containerViews(setup.data, settings).find(
    (v) => v.container.key === container.key,
  );
  if (!base) throw new Error('Missing fixture view');
  return {
    ...base,
    container: { ...base.container, ...patch },
    item: item ?? base.item,
  };
}
function fresh(): Setup {
  const data = {
    ...reconciliation,
    inventory: {
      ...reconciliation.inventory,
      observedAt: new Date().toISOString(),
    },
  };
  const [view] = containerViews(data, settings);
  if (!view) throw new Error('Missing fixture view');
  return { view, data, jobs: [], activityLoaded: true, now: Date.now() };
}
const importReady = (s: Setup) => {
  s.view = viewWith(s, {}, importable);
};
const withData = (s: Setup, patch: Partial<Reconciliation['inventory']>) => {
  s.data = { ...s.data, inventory: { ...s.data.inventory, ...patch } };
};
const readyFor = (
  s: Setup,
  patch: Partial<MaintenanceReadiness> = {},
): MaintenanceReadiness => ({
  key: s.view.container.key,
  identity: s.view.container.identity,
  fingerprint: s.view.container.fingerprint,
  observedAt: new Date(s.now - 1000).toISOString(),
  connected: true,
  distribution: 'debian',
  version: '13',
  source: 'ssh',
  appUpdate: true,
  osUpdate: true,
  reason: '',
  appReason: '',
  osReason: '',
  ...patch,
});

type Case = {
  name: string;
  action: ActionId;
  setup?: (s: Setup) => void;
  reason: string | RegExp;
};
// An empty reason means the action is allowed. Every blocking rule that existed
// before the module is listed here so none can be lost silently.
const cases: Case[] = [
  // Network change
  { name: 'network: healthy', action: 'networkChange', reason: '' },
  {
    name: 'network: incomplete inventory',
    action: 'networkChange',
    setup: (s) => withData(s, { complete: false }),
    reason: /Complete inventory is required/,
  },
  {
    name: 'network: no reconciled item',
    action: 'networkChange',
    setup: (s) => {
      s.view = { ...s.view, item: undefined };
    },
    reason: /Provider state is unknown/,
  },
  {
    name: 'network: unknown provider state',
    action: 'networkChange',
    setup: (s) => {
      s.view = viewWith(s, {}, { ...importable, state: 'unknown' });
    },
    reason: /Provider state is unknown/,
  },
  {
    name: 'network: importable conflict',
    action: 'networkChange',
    setup: importReady,
    reason: /Review and import the existing configuration/,
  },
  {
    name: 'network: plain conflict',
    action: 'networkChange',
    setup: (s) => {
      s.view = viewWith(s, {}, { ...importable, importAvailable: false });
    },
    reason: /Resolve the conflicting identity/,
  },
  {
    name: 'network: unverified generation',
    action: 'networkChange',
    setup: (s) => {
      s.view = viewWith(s, { identity: '' });
    },
    reason: /generation is unverified/,
  },
  {
    name: 'network: unfinished operation',
    action: 'networkChange',
    setup: (s) => {
      s.data = { ...s.data, operations: [partial] };
    },
    reason: /incomplete network operation/,
  },
  {
    name: 'network: a succeeded operation does not block',
    action: 'networkChange',
    setup: (s) => {
      s.data = {
        ...s.data,
        operations: [{ ...partial, status: 'succeeded' }],
      };
    },
    reason: '',
  },
  {
    name: 'network: incomplete inventory outranks identity',
    action: 'networkChange',
    setup: (s) => {
      withData(s, { complete: false });
      s.view = viewWith(s, { identity: '' });
    },
    reason: /Complete inventory is required/,
  },

  // Add reservation
  {
    name: 'reservation: allowed on a missing record',
    action: 'addReservation',
    setup: (s) => {
      s.view = viewWith(
        s,
        {},
        {
          key: container.key,
          state: 'conflict',
          owned: false,
          canAddReservation: true,
          ...withIssues('', []),
        },
      );
    },
    reason: '',
  },
  {
    name: 'reservation: stale host inventory',
    action: 'addReservation',
    setup: (s) => {
      s.view = { ...s.view, stale: true };
    },
    reason: /Refresh current inventory before repairing a reservation/,
  },
  {
    name: 'reservation: incomplete inventory still blocks a server-allowed repair',
    action: 'addReservation',
    setup: (s) => {
      withData(s, { complete: false });
      s.view = viewWith(
        s,
        {},
        { ...importable, canAddReservation: true, importAvailable: false },
      );
    },
    reason: /Complete inventory is required/,
  },
  {
    name: 'reservation: unverified generation blocks a server-allowed repair',
    action: 'addReservation',
    setup: (s) => {
      s.view = viewWith(
        s,
        { identity: '' },
        { ...importable, canAddReservation: true, importAvailable: false },
      );
    },
    reason: /generation is unverified/,
  },
  {
    name: 'reservation: server refusal is shown verbatim',
    action: 'addReservation',
    setup: (s) => {
      s.view = viewWith(
        s,
        {},
        {
          key: container.key,
          state: 'healthy',
          owned: true,
          canAddReservation: false,
          reservationBlocked: 'A reservation already exists.',
          ...withIssues('', []),
        },
      );
    },
    reason: 'A reservation already exists.',
  },
  {
    name: 'reservation: refusal without server text',
    action: 'addReservation',
    reason: 'Reservation repair is unavailable.',
  },
  {
    name: 'reservation: server refusal and client guard merge into one reason',
    action: 'addReservation',
    setup: (s) => {
      s.view = viewWith(
        s,
        {},
        {
          ...importable,
          importAvailable: false,
          canAddReservation: false,
          reservationBlocked: 'Reservation 1 collides with this IP.',
        },
      );
    },
    reason:
      /^Reservation 1 collides with this IP\. Resolve the conflicting identity/,
  },

  // Import
  { name: 'import: allowed', action: 'import', setup: importReady, reason: '' },
  {
    name: 'import: stale host',
    action: 'import',
    setup: (s) => {
      importReady(s);
      s.view = { ...s.view, stale: true };
    },
    reason: /Current complete inventory is required/,
  },
  {
    name: 'import: incomplete inventory',
    action: 'import',
    setup: (s) => {
      importReady(s);
      withData(s, { complete: false });
    },
    reason: /Current complete inventory is required/,
  },
  {
    name: 'import: inventory older than five minutes',
    action: 'import',
    setup: (s) => {
      importReady(s);
      withData(s, { observedAt: new Date(s.now - 301_000).toISOString() });
    },
    reason: /Current complete inventory is required/,
  },
  {
    name: 'import: unparseable observation time',
    action: 'import',
    setup: (s) => {
      importReady(s);
      withData(s, { observedAt: 'not a time' });
    },
    reason: /Current complete inventory is required/,
  },
  {
    name: 'import: unverified generation',
    action: 'import',
    setup: (s) => {
      importReady(s);
      s.view = viewWith(s, { identity: '' }, importable);
    },
    reason: /Complete identity enrollment/,
  },
  {
    name: 'import: no linked host',
    action: 'import',
    setup: (s) => {
      importReady(s);
      s.view = { ...s.view, host: undefined };
    },
    reason: /linked host\/firewall profile are required/,
  },
  {
    name: 'import: no linked firewall',
    action: 'import',
    setup: (s) => {
      importReady(s);
      s.view = { ...s.view, firewall: undefined };
    },
    reason: /linked host\/firewall profile are required/,
  },
  {
    name: 'import: no observed IP',
    action: 'import',
    setup: (s) => {
      importReady(s);
      s.view = viewWith(s, { ip: '' }, importable);
    },
    reason: /linked host\/firewall profile are required/,
  },
  {
    name: 'import: no observed MAC',
    action: 'import',
    setup: (s) => {
      importReady(s);
      s.view = viewWith(s, { mac: '' }, importable);
    },
    reason: /linked host\/firewall profile are required/,
  },
  {
    name: 'import: template',
    action: 'import',
    setup: (s) => {
      importReady(s);
      s.view = viewWith(s, { template: true }, importable);
    },
    reason: /linked host\/firewall profile are required/,
  },
  {
    name: 'import: VMID outside every address profile',
    action: 'import',
    setup: (s) => {
      s.view = viewWith(s, { vmid: 900 }, importable);
    },
    reason: /do not match the configured host profile/,
  },
  {
    name: 'import: expected IP differs',
    action: 'import',
    setup: (s) => {
      s.view = viewWith(s, { expectedIp: '192.168.6.10' }, importable);
    },
    reason: /do not match the configured host profile/,
  },
  {
    name: 'import: expected MAC differs',
    action: 'import',
    setup: (s) => {
      s.view = viewWith(s, { expectedMac: 'd0:cc:37:00:06:99' }, importable);
    },
    reason: /do not match the configured host profile/,
  },
  {
    name: 'import: expected MAC differing only by case is accepted',
    action: 'import',
    setup: (s) => {
      s.view = viewWith(s, { expectedMac: 'D0:CC:37:00:06:04' }, importable);
    },
    reason: '',
  },
  {
    name: 'import: server hostProfileMismatch code',
    action: 'import',
    setup: (s) => {
      s.view = viewWith(
        s,
        {},
        {
          ...importable,
          ...withIssues(
            ['unownedConflict', 'hostProfileMismatch'],
            ['unowned reservation matches', 'any wording'],
          ),
        },
      );
    },
    reason: /do not match the configured host profile/,
  },
  {
    name: 'import: profile prose without the code does not match',
    action: 'import',
    setup: (s) => {
      s.view = viewWith(
        s,
        {},
        {
          ...importable,
          ...withIssues('unownedConflict', [
            'container does not match host profile',
          ]),
        },
      );
    },
    reason: '',
  },
  {
    name: 'import: activity not loaded',
    action: 'import',
    setup: (s) => {
      importReady(s);
      s.activityLoaded = false;
    },
    reason: /activity history is required/,
  },
  {
    name: 'import: update attempt in flight',
    action: 'import',
    setup: (s) => {
      importReady(s);
      s.attempt = 'requesting';
    },
    reason: /update is active or needs outcome inspection/,
  },
  {
    name: 'import: update attempt uncertain',
    action: 'import',
    setup: (s) => {
      importReady(s);
      s.attempt = 'uncertain';
    },
    reason: /update is active or needs outcome inspection/,
  },
  {
    name: 'import: active job',
    action: 'import',
    setup: (s) => {
      importReady(s);
      s.jobs = [{ ...job, key: container.key }];
    },
    reason: /update is active or needs outcome inspection/,
  },
  {
    name: 'import: unacknowledged uncertain job',
    action: 'import',
    setup: (s) => {
      importReady(s);
      s.jobs = [{ ...job, key: container.key, status: 'timedOut' }];
    },
    reason: /update is active or needs outcome inspection/,
  },
  {
    name: 'import: unfinished operation',
    action: 'import',
    setup: (s) => {
      importReady(s);
      s.data = { ...s.data, operations: [partial] };
    },
    reason:
      /incomplete network operation needs recovery in Activity before import/,
  },
  {
    name: 'import: no importable match',
    action: 'import',
    setup: (s) => {
      s.view = viewWith(s, {}, { ...importable, importAvailable: false });
    },
    reason: /No unambiguous matching configuration/,
  },
  {
    name: 'import: already owned',
    action: 'import',
    setup: (s) => {
      s.view = viewWith(s, {}, { ...importable, owned: true });
    },
    reason: /No unambiguous matching configuration/,
  },
  {
    name: 'import: unknown state',
    action: 'import',
    setup: (s) => {
      s.view = viewWith(s, {}, { ...importable, state: 'unknown' });
    },
    reason: /No unambiguous matching configuration/,
  },
  {
    name: 'import: no observed route',
    action: 'import',
    setup: (s) => {
      s.view = viewWith(s, {}, { ...importable, existingRoute: undefined });
    },
    reason: /No unambiguous matching configuration/,
  },
  {
    name: 'import: no existing resources',
    action: 'import',
    setup: (s) => {
      s.view = viewWith(s, {}, { ...importable, existingResources: [] });
    },
    reason: /No unambiguous matching configuration/,
  },
  {
    name: 'import: server blockers are joined verbatim',
    action: 'import',
    setup: (s) => {
      s.view = viewWith(
        s,
        {},
        { ...importable, importIssues: ['Two handles match.', 'No ACL.'] },
      );
    },
    reason: 'Two handles match. No ACL.',
  },

  // Accept observed changes
  {
    name: 'accept: allowed',
    action: 'accept',
    setup: (s) => {
      s.view = viewWith(s, {}, { ...orphanFor(), canAcceptChanges: true });
    },
    reason: '',
  },
  {
    name: 'accept: server refusal is shown verbatim',
    action: 'accept',
    setup: (s) => {
      s.view = viewWith(
        s,
        {},
        { ...orphanFor(), acceptBlocked: 'Router has unfinished work.' },
      );
    },
    reason: 'Router has unfinished work.',
  },
  {
    name: 'accept: refusal without server text',
    action: 'accept',
    reason: /Accepting observed changes is unavailable/,
  },
  {
    name: 'accept: network rules still apply',
    action: 'accept',
    setup: (s) => {
      s.view = viewWith(
        s,
        { identity: '' },
        { ...orphanFor(), canAcceptChanges: true },
      );
    },
    reason: /generation is unverified/,
  },

  // App and OS updates
  ...(['appUpdate', 'osUpdate'] satisfies ActionId[]).flatMap(
    (action): Case[] => [
      { name: `${action}: allowed`, action, reason: '' },
      {
        name: `${action}: update attempt in flight`,
        action,
        setup: (s) => {
          s.attempt = 'requesting';
        },
        reason: /Inspect the previous update request/,
      },
      {
        name: `${action}: activity not loaded`,
        action,
        setup: (s) => {
          s.activityLoaded = false;
        },
        reason: /Current job state is unavailable/,
      },
      {
        name: `${action}: stale host`,
        action,
        setup: (s) => {
          s.view = { ...s.view, stale: true };
        },
        reason: /Host inventory is stale/,
      },
      {
        name: `${action}: template`,
        action,
        setup: (s) => {
          s.view = viewWith(s, { template: true });
        },
        reason: /Templates cannot be updated/,
      },
      {
        name: `${action}: stopped`,
        action,
        setup: (s) => {
          s.view = viewWith(s, { status: 'stopped' });
        },
        reason: /require a running container/,
      },
      {
        name: `${action}: unverified generation`,
        action,
        setup: (s) => {
          s.view = viewWith(s, { identity: '' });
        },
        reason: /generation is unverified/,
      },
      {
        name: `${action}: SSH disabled`,
        action,
        setup: (s) => {
          s.view = viewWith(s, {});
          s.view = {
            ...s.view,
            host: s.view.host && {
              ...s.view.host,
              ssh: { ...s.view.host.ssh, enabled: false },
            },
          };
        },
        reason: /SSH updates are not enabled/,
      },
      {
        name: `${action}: active job`,
        action,
        setup: (s) => {
          s.jobs = [{ ...job, key: container.key, status: 'queued' }];
        },
        reason: /active or needs acknowledgement/,
      },
      {
        name: `${action}: unacknowledged reboot-required job`,
        action,
        setup: (s) => {
          s.jobs = [{ ...job, key: container.key, status: 'rebootRequired' }];
        },
        reason: /active or needs acknowledgement/,
      },
      {
        name: `${action}: acknowledged and failed jobs do not block`,
        action,
        setup: (s) => {
          s.jobs = [
            {
              ...job,
              key: container.key,
              status: 'timedOut',
              acknowledged: true,
            },
            { ...job, id: 'job-2', key: container.key, status: 'failed' },
          ];
        },
        reason: '',
      },
      {
        name: `${action}: another container's job does not block`,
        action,
        setup: (s) => {
          s.jobs = [{ ...job, key: 'main:999' }];
        },
        reason: '',
      },
      {
        name: `${action}: readiness-reported blocker`,
        action,
        setup: (s) => {
          s.readiness = readyFor(s, { reason: 'SSH key rejected.' });
        },
        reason: 'SSH key rejected.',
      },
      {
        name: `${action}: stale readiness is ignored`,
        action,
        setup: (s) => {
          s.readiness = readyFor(s, {
            reason: 'SSH key rejected.',
            observedAt: new Date(s.now - 61_000).toISOString(),
          });
        },
        reason: '',
      },
      {
        name: `${action}: readiness for another generation is ignored`,
        action,
        setup: (s) => {
          s.readiness = readyFor(s, {
            reason: 'SSH key rejected.',
            identity: 'other-generation',
          });
        },
        reason: '',
      },
    ],
  ),
  {
    name: 'appUpdate: readiness app blocker',
    action: 'appUpdate',
    setup: (s) => {
      s.readiness = readyFor(s, { appReason: 'No update script.' });
    },
    reason: 'No update script.',
  },
  {
    name: 'appUpdate: OS-only readiness blocker does not block app updates',
    action: 'appUpdate',
    setup: (s) => {
      s.readiness = readyFor(s, { osReason: 'Unsupported package manager.' });
    },
    reason: '',
  },
  {
    name: 'osUpdate: readiness OS blocker',
    action: 'osUpdate',
    setup: (s) => {
      s.readiness = readyFor(s, { osReason: 'Unsupported package manager.' });
    },
    reason: 'Unsupported package manager.',
  },
  {
    name: 'osUpdate: unknown distribution',
    action: 'osUpdate',
    setup: (s) => {
      s.view = viewWith(s, { distribution: 'unknown' });
    },
    reason: /OS update support is unknown/,
  },
  {
    name: 'osUpdate: SSH-detected distribution replaces an unknown guess',
    action: 'osUpdate',
    setup: (s) => {
      s.view = viewWith(s, { distribution: 'unknown' });
      s.readiness = readyFor(s, { distribution: 'ubuntu' });
    },
    reason: '',
  },
  {
    name: 'osUpdate: unsupported SSH-detected distribution',
    action: 'osUpdate',
    setup: (s) => {
      s.readiness = readyFor(s, { distribution: 'arch' });
    },
    reason: /OS update support is unknown/,
  },
  {
    name: 'appUpdate: unknown distribution does not block app updates',
    action: 'appUpdate',
    setup: (s) => {
      s.view = viewWith(s, { distribution: 'unknown' });
    },
    reason: '',
  },

  // Readiness check
  { name: 'readiness: allowed', action: 'readinessCheck', reason: '' },
  {
    name: 'readiness: stale host',
    action: 'readinessCheck',
    setup: (s) => {
      s.view = { ...s.view, stale: true };
    },
    reason: /Host inventory is stale/,
  },
  {
    name: 'readiness: stopped',
    action: 'readinessCheck',
    setup: (s) => {
      s.view = viewWith(s, { status: 'stopped' });
    },
    reason: /require a running container/,
  },
  {
    name: 'readiness: unverified generation',
    action: 'readinessCheck',
    setup: (s) => {
      s.view = viewWith(s, { identity: '' });
    },
    reason: /generation is unverified/,
  },

  // Cleanup
  {
    name: 'cleanup: a live healthy container',
    action: 'cleanup',
    reason: /Cleanup requires a confirmed owned orphan/,
  },
  {
    name: 'cleanup: owned orphan',
    action: 'cleanup',
    setup: (s) => {
      s.view = viewWith(s, {}, orphanFor());
    },
    reason: '',
  },
  {
    name: 'cleanup: selectable owned resources',
    action: 'cleanup',
    setup: (s) => {
      s.view = viewWith(
        s,
        {},
        { ...orphanFor(), state: 'conflict', cleanupSelectable: true },
      );
    },
    reason: '',
  },
  {
    name: 'cleanup: unowned orphan-looking record',
    action: 'cleanup',
    setup: (s) => {
      s.view = viewWith(s, {}, { ...orphanFor(), owned: false });
    },
    reason: /Cleanup requires a confirmed owned orphan/,
  },
  {
    name: 'cleanup: incomplete inventory',
    action: 'cleanup',
    setup: (s) => {
      s.view = viewWith(s, {}, orphanFor());
      withData(s, { complete: false });
    },
    reason: /Cleanup requires complete fresh inventory/,
  },
  {
    name: 'cleanup: unfinished operation',
    action: 'cleanup',
    setup: (s) => {
      s.view = viewWith(s, {}, orphanFor());
      s.data = { ...s.data, operations: [partial] };
    },
    reason: /network operation is incomplete/,
  },
  {
    name: 'cleanup: no reconciled item',
    action: 'cleanup',
    setup: (s) => {
      s.view = { ...s.view, item: undefined };
    },
    reason: /No provider record is available for cleanup/,
  },
];
function orphanFor(): ReconcileItem {
  return { ...orphan, key: container.key };
}

describe('action eligibility', () => {
  it.each(cases)('$name', ({ action, setup, reason }) => {
    const s = fresh();
    setup?.(s);
    const result = eligibility(s.view, {
      data: s.data,
      jobs: s.jobs,
      activityLoaded: s.activityLoaded,
      jobAttemptState: s.attempt,
      readiness: s.readiness,
      now: s.now,
    })[action];
    if (reason === '') {
      expect(result).toEqual({ allowed: true });
    } else {
      expect(result.allowed).toBe(false);
      expect(reasonOf(result)).toMatch(reason);
      // One reason, never a list or an empty placeholder.
      expect(reasonOf(result).trim()).not.toBe('');
    }
  });

  it('covers every action id in the table', () => {
    const covered = new Set(cases.map((c) => c.action));
    const s = fresh();
    const all = Object.keys(
      eligibility(s.view, {
        data: s.data,
        jobs: [],
        activityLoaded: true,
        now: s.now,
      }),
    );
    expect(all.sort()).toEqual([...covered].sort());
  });

  it('gives cards, rows and the inspector one verdict from identical inputs', () => {
    const s = fresh();
    s.view = viewWith(s, { status: 'stopped' });
    const context = {
      data: s.data,
      jobs: s.jobs,
      activityLoaded: true,
      now: s.now,
    };
    expect(eligibility(s.view, context)).toEqual(
      eligibility(s.view, { ...context }),
    );
  });

  it('evaluates cleanup for orphans that have no container view', () => {
    const s = fresh();
    expect(cleanupEligibility(orphan, s.data)).toEqual({ allowed: true });
    expect(
      cleanupEligibility({ ...orphan, state: 'unknown' }, s.data).allowed,
    ).toBe(false);
  });

  it('only trusts readiness for the same generation within a minute', () => {
    const s = fresh();
    const ready = readyFor(s);
    expect(currentReadiness(ready, s.view.container, s.now)).toBe(ready);
    expect(currentReadiness(null, s.view.container, s.now)).toBeNull();
    expect(
      currentReadiness(ready, s.view.container, s.now + 61_000),
    ).toBeNull();
    expect(
      currentReadiness(
        { ...ready, fingerprint: 'other' },
        s.view.container,
        s.now,
      ),
    ).toBeNull();
  });
});
