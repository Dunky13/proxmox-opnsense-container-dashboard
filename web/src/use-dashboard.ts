import { useCallback, useEffect, useRef, useState } from 'react';
import { Api, ApiError, messageOf } from './api';
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

export type DashboardRead =
  'configuration' | 'provider inventory' | 'jobs' | 'operations';
export type ReadState =
  'pending' | 'loaded' | 'failed' | 'cancelled' | 'superseded';
export type DashboardReads = Record<DashboardRead, ReadState>;

export function useDashboard(api: Api) {
  const [settings, updateSettings] = useState<Settings | null>(null);
  const settingsRef = useRef<Settings | null>(null);
  const [inventorySettings, setInventorySettings] = useState<Settings | null>(
    null,
  );
  const [data, setData] = useState<Reconciliation | null>(null);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [operations, setOperations] = useState<Operation[]>([]);
  const [error, setError] = useState('');
  const [activityError, setActivityError] = useState('');
  const [loading, setLoading] = useState(true);
  const [pending, setPending] = useState<DashboardRead[]>([]);
  const [refreshId, setRefreshId] = useState(0);
  const [reads, setReads] = useState<DashboardReads | null>(null);
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
    setRefreshId(current);
    const activityVersion = ++activityEpoch.current;
    refreshController.current?.abort();
    const controller = new AbortController();
    refreshController.current = controller;
    setLoading(true);
    setInventoryValid(false);
    setActivityLoaded(false);
    setError('');
    setActivityError('');
    const labels: DashboardRead[] = [
      'configuration',
      'provider inventory',
      'jobs',
      'operations',
    ];
    setPending(labels);
    setReads({
      configuration: 'pending',
      'provider inventory': 'pending',
      jobs: 'pending',
      operations: 'pending',
    });
    const isCurrent = () =>
      alive.current &&
      current === generation.current &&
      !controller.signal.aborted;
    let config: Settings | null = null;
    let inventory: Reconciliation | null = null;
    let jobsLoaded = false;
    let operationsLoaded = false;
    const failures = new Map<string, string>();
    const activityFailures = new Map<string, string>();
    function publishInventory() {
      // Pair only this generation's configuration and inventory. Early settings
      // may update navigation, but never authorize the previous observation.
      if (!isCurrent() || !config || !inventory) return;
      const next = inventory;
      const retain = observationRevision.current === config.revision;
      setData((previous) => retainObservations(retain ? previous : null, next));
      setInventorySettings(config);
      observationRevision.current = config.revision;
      setInventoryValid(true);
    }
    async function read<T>(
      label: DashboardRead,
      request: Promise<T>,
      apply: (value: T) => void,
      activity = false,
    ) {
      try {
        const value = await request;
        if (isCurrent()) {
          if (activity && activityVersion !== activityEpoch.current)
            setReads((old) => old && { ...old, [label]: 'superseded' });
          else {
            apply(value);
            setReads((old) => old && { ...old, [label]: 'loaded' });
          }
        }
      } catch (err) {
        if (!isCurrent()) return;
        if (activity && activityVersion !== activityEpoch.current) {
          setReads((old) => old && { ...old, [label]: 'superseded' });
          return;
        }
        setReads((old) => old && { ...old, [label]: 'failed' });
        const detail = messageOf(err instanceof Error ? err : null);
        const message =
          err instanceof ApiError && err.code === 'timeout'
            ? `${label}: ${detail}`
            : detail;
        if (activity) {
          activityFailures.set(label, message);
          setActivityError(
            `${[...activityFailures.values()].join(' ')} Previously observed activity may be stale. Retry refresh before maintenance.`,
          );
          setActivityLoaded(false);
        } else {
          failures.set(label, message);
          setError([...failures.values()].join(' '));
        }
      } finally {
        if (isCurrent())
          setPending((old) => old.filter((item) => item !== label));
      }
    }
    const activityReady = () => {
      if (
        activityVersion === activityEpoch.current &&
        jobsLoaded &&
        operationsLoaded
      )
        setActivityLoaded(true);
    };
    await Promise.all([
      read(
        'configuration',
        api.request('/settings', settingsSchema, { signal: controller.signal }),
        (value) => {
          if (
            settingsRef.current &&
            value.revision < settingsRef.current.revision
          )
            throw new ApiError(
              'Configuration changed. Retry refresh to load the current revision.',
            );
          config = value;
          settingsRef.current = value;
          updateSettings(value);
          publishInventory();
        },
      ),
      read(
        'provider inventory',
        api.request('/reconciliation', reconciliationSchema, {
          signal: controller.signal,
        }),
        (value) => {
          inventory = value;
          publishInventory();
        },
      ),
      read(
        'jobs',
        api.request('/jobs', jobsSchema, { signal: controller.signal }),
        (value) => {
          setJobs(value);
          jobsLoaded = true;
          activityReady();
        },
        true,
      ),
      read(
        'operations',
        api.request('/operations', operationsSchema, {
          signal: controller.signal,
        }),
        (value) => {
          setOperations(value);
          operationsLoaded = true;
          activityReady();
        },
        true,
      ),
    ]);
    if (isCurrent()) {
      refreshController.current = null;
      setLoading(false);
    }
  }, [api]);
  const cancelRefresh = useCallback(() => {
    generation.current++;
    activityEpoch.current++;
    refreshController.current?.abort();
    refreshController.current = null;
    setLoading(false);
    setPending([]);
    const cancelled = (state: ReadState): ReadState =>
      state === 'pending' ? 'cancelled' : state;
    setReads(
      (old) =>
        old && {
          configuration: cancelled(old.configuration),
          'provider inventory': cancelled(old['provider inventory']),
          jobs: cancelled(old.jobs),
          operations: cancelled(old.operations),
        },
    );
    setError(
      'Refresh cancelled. Completed reads have been kept. Retry refresh for missing data.',
    );
  }, []);
  const setSettings = useCallback((next: Settings) => {
    // A settings write supersedes any older in-flight read and its readiness.
    generation.current++;
    activityEpoch.current++;
    refreshController.current?.abort();
    refreshController.current = null;
    settingsRef.current = next;
    updateSettings(next);
    setInventoryValid(false);
    setActivityLoaded(false);
    setLoading(false);
    setPending([]);
    setReads(null);
  }, []);
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
        } catch (err) {
          if (
            !controller.signal.aborted &&
            activityVersion === activityEpoch.current
          ) {
            setActivityLoaded(false);
            setActivityError(messageOf(err instanceof Error ? err : null));
          }
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
    inventorySettings,
    data,
    jobs,
    operations,
    error,
    activityError,
    activityLoaded,
    inventoryValid,
    loading,
    pending,
    refreshId,
    reads,
    refresh,
    cancelRefresh,
    addJob,
  };
}
