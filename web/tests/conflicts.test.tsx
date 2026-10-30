import { fireEvent, render, screen, within } from '@testing-library/react';
import { expect, it } from 'vitest';
import { ContainerIssues } from '../src/ContainerIssues';
import { containerViews } from '../src/viewmodel';
import { container, reconciliation, settings } from './fixtures';

for (const ct of [
  {
    vmid: 200,
    name: 'dbug-git',
    ip: '192.168.6.120',
    mac: 'd0:cc:37:00:02:20',
  },
  {
    vmid: 201,
    name: 'forgejo-docker',
    ip: '192.168.6.121',
    mac: 'd0:cc:37:00:02:21',
  },
]) {
  it(`explains CT${ct.vmid}'s actual legacy layout and safe identity enrollment separately`, () => {
    const config = {
      ...settings,
      hosts: settings.hosts.map((h) => ({
        ...h,
        vmidMin: 603,
        addressProfiles: [
          {
            vmidMin: 701,
            vmidMax: 799,
            network: '192.168.7.0/24',
            macPrefix: 'd0:cc:37:00:07',
          },
        ],
      })),
    };
    const [view] = containerViews(
      {
        ...reconciliation,
        inventory: {
          ...reconciliation.inventory,
          containers: [
            {
              ...container,
              ...ct,
              key: `main:${ct.vmid}`,
              identity: '',
              expectedIp: undefined,
              expectedMac: undefined,
              warnings: [
                'creation identity unavailable; add a unique container-dashboard-generation UUID marker before mutations',
              ],
            },
          ],
        },
        items: [
          {
            key: `main:${ct.vmid}`,
            state: 'conflict',
            owned: false,
            issues: ['container does not match host profile'],
          },
        ],
      },
      config,
    );
    if (!view) throw new Error('Missing fixture view');
    render(<ContainerIssues view={view} />);
    expect(
      screen.getByText('container does not match host profile'),
    ).toBeVisible();
    fireEvent.click(screen.getByText('Why blocked? Address rules'));
    expect(
      screen.getByText(
        `VMID ${ct.vmid} is outside every configured address-profile range.`,
      ),
    ).toBeVisible();
    expect(screen.getByText(ct.ip)).toBeVisible();
    expect(screen.getByText(ct.mac)).toBeVisible();
    expect(screen.getByText('603 to 699')).toBeVisible();
    expect(screen.getByText('701 to 799')).toBeVisible();
    expect(screen.getByText('192.168.6.0/24')).toBeVisible();
    expect(screen.getByText('d0:cc:37:00:06')).toBeVisible();
    fireEvent.click(screen.getByText('Verification help'));
    expect(screen.getByLabelText('Linux UUID command')).toHaveValue(
      'cat /proc/sys/kernel/random/uuid',
    );
    expect(
      screen.getByText('container-dashboard-generation=<UUID>'),
    ).toBeVisible();
    expect(screen.getByText(/preserve existing notes/i)).toBeVisible();
    expect(
      screen.getByText(/new UUID for each recreated or cloned container/i),
    ).toBeVisible();
    expect(
      screen.getByText(/does not fix an address-rule mismatch/i),
    ).toBeVisible();
    expect(
      screen.getByText(/maintenance also requires configured SSH/i),
    ).toBeVisible();
    expect(screen.queryByText('Expected IP address')).not.toBeInTheDocument();
  });
}

it('preserves foreign-resource conflict reasons without inventing a profile mismatch', () => {
  const [view] = containerViews(
    {
      ...reconciliation,
      items: [
        {
          key: container.key,
          state: 'conflict',
          owned: false,
          issues: [
            'unowned reservation or domain conflicts',
            'container identity changed or unavailable',
          ],
        },
      ],
    },
    settings,
  );
  if (!view) throw new Error('Missing fixture view');
  render(<ContainerIssues view={view} />);
  expect(
    screen.getByText('unowned reservation or domain conflicts'),
  ).toBeVisible();
  expect(
    screen.getByText('container identity changed or unavailable'),
  ).toBeVisible();
  expect(
    screen.queryByText('Why blocked? Address rules'),
  ).not.toBeInTheDocument();
  expect(
    within(screen.getByRole('list')).getAllByRole('listitem'),
  ).toHaveLength(2);
});
