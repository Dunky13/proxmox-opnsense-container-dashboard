import { useCallback, useEffect, useRef, useState } from 'react';
import { Api, messageOf } from './api';
import {
  jobsSchema,
  operationsSchema,
  reconciliationSchema,
  settingsSchema,
  type Job,
  type Operation,
  type Reconciliation,
  type Settings,
} from './schemas';
import { activeJob } from './viewmodel';
import { retainObservations } from './inventory-observations';

export function useDashboard(api: Api) {
  const [settings, setSettings] = useState<Settings | null>(null);
  const [data, setData] = useState<Reconciliation | null>(null);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [operations, setOperations] = useState<Operation[]>([]);
  const [error, setError] = useState('');
  const [activityError, setActivityError] = useState('');
  const [loading, setLoading] = useState(true);
  const [activityLoaded, setActivityLoaded] = useState(false);
  const [inventoryValid, setInventoryValid] = useState(false);
  const observationRevision = useRef<number | null>(null);
  const generation = useRef(0);
  const activityEpoch = useRef(0);
  const refreshController = useRef<AbortController | null>(null);
  const alive = useRef(true);
  const refresh = useCallback(async () => {
    if (!alive.current) return;
    const current = ++generation.current;
    const activityVersion = ++activityEpoch.current;
    refreshController.current?.abort();
    const controller = new AbortController();
    refreshController.current = controller;
    setLoading(true);
    setInventoryValid(false);
    const results = await Promise.allSettled([
      api.request('/settings', settingsSchema, { signal: controller.signal }),
      api.request('/reconciliation', reconciliationSchema, {
        signal: controller.signal,
      }),
      api.request('/jobs', jobsSchema, { signal: controller.signal }),
      api.request('/operations', operationsSchema, {
        signal: controller.signal,
      }),
    ]);
    if (
      !alive.current ||
      current !== generation.current ||
      controller.signal.aborted
    )
      return;
    const [config, inventory, updates, ops] = results;
    const errors: string[] = [];
    if (config.status === 'fulfilled') setSettings(config.value);
    else
      errors.push(
        messageOf(config.reason instanceof Error ? config.reason : null),
      );
    if (inventory.status === 'fulfilled') {
      const retain =
        config.status === 'fulfilled' &&
        observationRevision.current === config.value.revision;
      setData((previous) =>
        retainObservations(retain ? previous : null, inventory.value),
      );
      observationRevision.current =
        config.status === 'fulfilled' ? config.value.revision : null;
      setInventoryValid(true);
    } else
      errors.push(
        messageOf(inventory.reason instanceof Error ? inventory.reason : null),
      );
    if (activityVersion === activityEpoch.current) {
      if (updates.status === 'fulfilled') setJobs(updates.value);
      if (ops.status === 'fulfilled') setOperations(ops.value);
    }
    setActivityLoaded(
      updates.status === 'fulfilled' && ops.status === 'fulfilled',
    );
    const activityFailure =
      updates.status === 'rejected' || ops.status === 'rejected';
    setActivityError(
      activityFailure
        ? 'Activity could not be refreshed. Previously observed jobs and operations may be stale.'
        : '',
    );
    setError([...new Set(errors)].join(' '));
    setLoading(false);
  }, [api]);
  useEffect(() => {
    alive.current = true;
    void refresh();
    return () => {
      alive.current = false;
      generation.current++;
      refreshController.current?.abort();
    };
  }, [refresh]);
  const pollJobs = jobs.some(activeJob);
  const pollOperations = operations.some((op) => op.status === 'running');
  useEffect(() => {
    if (!pollJobs && !pollOperations) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    async function poll() {
      const activityVersion = activityEpoch.current;
      if (!controller.signal.aborted && document.visibilityState !== 'hidden') {
        try {
          if (pollJobs) {
            const next = await api.request('/jobs', jobsSchema, {
              signal: controller.signal,
            });
            if (
              !controller.signal.aborted &&
              activityVersion === activityEpoch.current
            )
              setJobs(next);
          }
          if (pollOperations) {
            const next = await api.request('/operations', operationsSchema, {
              signal: controller.signal,
            });
            if (
              !controller.signal.aborted &&
              activityVersion === activityEpoch.current
            )
              setOperations(next);
          }
          if (!controller.signal.aborted) setActivityError('');
        } catch (err) {
          if (!controller.signal.aborted)
            setActivityError(messageOf(err instanceof Error ? err : null));
        }
      }
      if (!controller.signal.aborted)
        timer = setTimeout(() => {
          void poll();
        }, 2500);
    }
    timer = setTimeout(() => {
      void poll();
    }, 2500);
    return () => {
      controller.abort();
      clearTimeout(timer);
    };
  }, [api, pollJobs, pollOperations]);
  const addJob = (job: Job) => {
    if (!alive.current) return;
    activityEpoch.current++;
    setJobs((old) => [job, ...old.filter((j) => j.id !== job.id)]);
  };
  return {
    settings,
    setSettings,
    data,
    jobs,
    operations,
    error,
    activityError,
    activityLoaded,
    inventoryValid,
    loading,
    refresh,
    addJob,
  };
}
