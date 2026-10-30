# Product

## Register

product

## Users

Marc, administering a personal Proxmox and OPNsense homelab. The primary workflow is checking containers across hosts, preparing their network configuration, reviewing drift after container removal, and requesting maintenance without repeating manual firewall setup.

## Product Purpose

Container Proxy connects Proxmox inventory to KEA DHCP reservations and Caddy reverse proxies on the associated OPNsense firewall. Multiple Proxmox hosts can share one firewall, and multiple firewalls can be configured. Containers remain visible whether running or stopped. The smallest available VMID in each host's configured range is always easy to find.

Success means the administrator can understand the observed state, inspect proposed changes, and maintain container access without inadvertently exposing services or deleting unrelated configuration. The deliverable is a Go service with an embedded web UI that can run as one binary in an LXC container.

## Brand Personality

Calm, compact, precise. The user selected a direction closer to Linear and Raycast, with system light/dark mode. Use familiar product controls and plain operational language. No marketing presentation is needed.

## Anti-references

The user has no specific disliked interfaces. Avoid decorative dashboards, invented health metrics, misleading success states, excessive animation, and controls that conceal the effect of a network change.

## Design Principles

- Keep both the fleet and the next available IDs easy to scan.
- Distinguish observed provider state from desired configuration and demo records.
- Make private LAN/VPN access the default; make public exposure deliberate.
- Show the scope and result of a change, including partial failures.
- Keep maintenance actions close to the container they affect.
- Discover KEA subnets and match each container's intended address automatically, including different VLANs on one firewall. Do not require the administrator to look up subnet UUIDs.
- Fetch existing Caddy access lists for selection; preserve shared lists without taking ownership of them.
- Use Caddy's existing automatic HTTPS configuration without requiring a certificate reference during setup.
- Configure upstream scheme and port per app, independently of the linked OPNsense configuration and other apps.

## Accessibility & Inclusion

Use keyboard-accessible controls, readable contrast, visible focus, reduced-motion support, and textual state labels that do not depend on color. The user reported no additional accessibility needs. Adapt the whole workflow to narrow screens rather than hiding configuration or maintenance actions.

## Access

The user selected both OIDC and a configured administrator token. Use an established OIDC implementation with explicit administrator authorization. Keep token entry out of URLs and persistent browser storage. Provider credentials belong on the server and must not appear in settings responses or logs.
