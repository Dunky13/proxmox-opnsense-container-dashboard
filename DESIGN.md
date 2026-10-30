# Container Proxy design system

## Direction

The user approved the sidebar and detail inspector from probe B, combined with a Cards/Rows switch. Carry across the calm, compact density; readable container identity and address hierarchy; persistent next-free host VMIDs; and restrained green selection/action color. Both views share filtering, selection, configuration, and maintenance behavior.

The images are composition references. Do not copy illustrative provider labels, dates, IPs, or unverified update-availability claims. Core UI remains semantic and editable, not raster imagery.

## Color and theme

Follow system preference by default. Allow Light, Dark, and System under appearance settings. Apply a single theme to the full app. Use OKLCH semantic CSS variables; lightly tint neutrals toward green. Use accent for selected navigation, focus, and primary actions. Status colors supplement textual labels.

Light: off-white canvas, subtly tinted sidebar, near-white card surfaces, dark graphite text, forest-green actions. Dark: graphite canvas, distinct raised surfaces, soft-white text, a lighter restrained green action. Avoid pure black/white, glass, gradients, colored side stripes, and ornamental shadows. Verify contrast in both themes.

## Type and spacing

Use a self-hosted readable sans family, preferably Geist, with a native sans fallback. Fixed rem typography for stable product controls. Monospace is reserved for IPs, MACs, VMIDs, timestamps and execution output. Use tabular numerals where data aligns. Use a 4px spacing base with semantic tokens and extra separation between host groups.

## Shape and elevation

Controls use 6px corners; cards and panels use 10px corners; status labels use compact rounded corners. Prefer borders and surface changes to shadows. Host headings sit outside cards. No card wrappers around whole host sections or nested decorative cards.

## Components

- One icon family, Phosphor. App identification can use bundled verified app icons with a neutral fallback; inference must remain labeled.
- Base UI for complex accessible controls where native HTML is insufficient. Avoid extra libraries for effects that simple CSS can implement.
- Shared buttons, labeled fields, feedback, badges, container identity, and the detail inspector across routes and both inventory views.
- Search, host, state, distribution and exposure filters compose. Cards/Rows is an accessible pressed-button group or radio group, with persistent preference and unchanged filter/selection state.
- Distinguish running/stopped, private/public, managed/conflicting/unknown, pending/failed/partial states with text.
- Destructive or exposure-changing plans show affected resources and an explicit action. Do not optimistically announce network mutation success.

## Responsive behavior

Desktop: sidebar plus header, a card grid or row list with detail inspector. Small laptop/tablet: compact navigation and narrower grid; inspector moves below when needed. Narrow mobile: accessible collapsible navigation, wrapping next-ID indicators, single-column cards or compact stacked rows, inline details. No viewport-wide horizontal scroll or hidden maintenance/configuration functions.

## Motion and accessibility

Only short feedback/state transitions, approximately 150ms. No entry choreography, bounce, or infinite decoration. Respect reduced motion. Provide visible focus, labels above fields, associated errors, keyboard operation, meaningful disabled explanations, stable skeletons, first-run guidance, and no-match/error recovery.

## Layers

Use a small documented layer scale: content 0, sticky header/navigation 10, floating controls 20, transient feedback 30. Avoid arbitrary z-index values; prefer inline configuration and detail inspection to modal overlays.
