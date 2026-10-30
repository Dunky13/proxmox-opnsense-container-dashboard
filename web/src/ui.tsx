import { useId, type ReactNode } from 'react';
import {
  ChartBar,
  Cube,
  Database,
  GitBranch,
  House,
  PlayCircle,
  ShieldCheck,
  WarningCircle,
} from '@phosphor-icons/react';
import { stateLabel } from './viewmodel';

export function Badge({
  children,
  tone = 'neutral',
}: {
  children: ReactNode;
  tone?: string;
}) {
  return <span className={`badge ${tone}`}>{children}</span>;
}
export function Status({ value }: { value: string }) {
  const tone = ['healthy', 'running', 'succeeded'].includes(value)
    ? 'good'
    : ['failed', 'conflict', 'interrupted', 'timedOut'].includes(value)
      ? 'danger'
      : [
            'missing',
            'mismatch',
            'partial',
            'ownedOrphan',
            'rebootRequired',
            'unknown',
          ].includes(value)
        ? 'warning'
        : 'neutral';
  return <Badge tone={tone}>{stateLabel(value)}</Badge>;
}
export function Notice({
  children,
  tone = 'warning',
  role,
}: {
  children: ReactNode;
  tone?: string;
  role?: 'alert' | 'status';
}) {
  return (
    <div className={`notice ${tone}`} role={role}>
      <WarningCircle size={18} aria-hidden="true" />
      <div>{children}</div>
    </div>
  );
}
export function Empty({
  title,
  children,
}: {
  title: string;
  children: ReactNode;
}) {
  return (
    <div className="empty">
      <Cube size={28} aria-hidden="true" />
      <h2>{title}</h2>
      <div>{children}</div>
    </div>
  );
}
export function Field({
  label,
  help,
  children,
}: {
  label: string;
  help?: string;
  children: (id: string, helpId?: string) => ReactNode;
}) {
  const id = useId();
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      {children(id, help ? `${id}-help` : undefined)}
      {help && <small id={`${id}-help`}>{help}</small>}
    </div>
  );
}
export function AppIcon({ app }: { app: string }) {
  const Icon =
    app === 'grafana'
      ? ChartBar
      : ['redis', 'postgres'].includes(app)
        ? Database
        : ['forgejo', 'gitea'].includes(app)
          ? GitBranch
          : app === 'homeassistant'
            ? House
            : ['jellyfin', 'plex'].includes(app)
              ? PlayCircle
              : app === 'adguard'
                ? ShieldCheck
                : Cube;
  return (
    <span
      className="app-icon"
      title={
        app && app !== 'unknown' ? `${app}, inferred from name` : 'Container'
      }
    >
      <Icon size={24} aria-hidden="true" />
    </span>
  );
}
export function Skeleton() {
  return (
    <div
      aria-busy="true"
      aria-label="Loading inventory"
      className="skeleton-stack"
    >
      <div className="skeleton line" />
      {[1, 2, 3].map((i) => (
        <div className="skeleton row" key={i} />
      ))}
      <span className="sr-only">
        Reading configured hosts and container inventory.
      </span>
    </div>
  );
}
