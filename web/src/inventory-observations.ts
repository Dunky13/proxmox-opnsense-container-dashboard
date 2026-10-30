import type { ReconcileItem, Reconciliation } from './schemas';

// Keep observations for display only. Current incomplete status always wins
// over cached allocation, reconciliation and mutation eligibility.
export function retainObservations(
  previous: Reconciliation | null,
  incoming: Reconciliation,
): Reconciliation {
  const failed = new Set(
    incoming.inventory.hosts.filter((h) => !h.complete).map((h) => h.hostId),
  );
  const hosts = incoming.inventory.hosts.map((host) => ({
    ...host,
    nextVmid: host.complete ? host.nextVmid : null,
    observedAt: host.complete
      ? incoming.inventory.observedAt
      : (previous?.inventory.hosts.find((h) => h.hostId === host.hostId)
          ?.observedAt ??
        previous?.inventory.observedAt ??
        incoming.inventory.observedAt),
  }));
  const containers = new Map(
    previous?.inventory.containers
      .filter((c) => failed.has(c.hostId))
      .map((c) => [c.key, c]),
  );
  for (const container of incoming.inventory.containers)
    containers.set(container.key, container);
  const items = new Map(
    previous?.items
      .filter(
        (item) =>
          failed.has(item.key.split(':')[0] ?? '') && containers.has(item.key),
      )
      .map((item) => [item.key, item]),
  );
  for (const item of incoming.items) items.set(item.key, item);
  return {
    ...incoming,
    inventory: {
      ...incoming.inventory,
      hosts,
      containers: [...containers.values()].map((container) => {
        if (!failed.has(container.hostId)) return container;
        const observedAt = hosts.find(
          (h) => h.hostId === container.hostId,
        )?.observedAt;
        return {
          ...container,
          warnings: [
            ...new Set([
              ...container.warnings,
              `Host inventory is stale. Last observed values: ${observedAt}. Refresh before changing this container.`,
            ]),
          ],
        };
      }),
    },
    items: [...items.values()].map((item): ReconcileItem => {
      if (!failed.has(item.key.split(':')[0] ?? '')) return item;
      return {
        ...item,
        state: 'unknown',
        issues: [
          ...new Set([
            ...item.issues,
            'Current host inventory is incomplete; previous observations are display only.',
          ]),
        ],
      };
    }),
  };
}

export function invalidateObservations(
  data: Reconciliation,
  reason: string,
): Reconciliation {
  return retainObservations(data, {
    ...data,
    inventory: {
      ...data.inventory,
      complete: false,
      hosts: data.inventory.hosts.map((host) => ({
        ...host,
        complete: false,
        nextVmid: null,
        error: reason,
      })),
    },
  });
}
