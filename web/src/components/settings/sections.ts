// The Settings area: THE one list. Seven groups (#/settings/<group>); a group is either a page of stacked cards or a
// set of tabs (#/settings/<group>/<tab>). A new page is one component plus one line here; the shell does not change.
import type { Component } from 'svelte';

type Loader = () => Promise<{ default: Component<any> }>;

/** The mode settings pages, in the order of the modes list. */
const MODE_TABS: [string, string][] = [['allstar', 'AllStar'], ['echolink', 'EchoLink'], ['bm', 'BrandMeister'], ['dmr', 'DMR'], ['tgif', 'TGIF'], ['ysf', 'YSF'], ['p25', 'P25'], ['nxdn', 'NXDN'], ['m17', 'M17']];

export interface SettingsTab {
  /** URL part: #/settings/<group>/<id>. Lowercase letters, digits and hyphens. */
  id: string;
  label: string;
  /** Longer name of the page; the shell draws no heading, so the page uses it itself if it wants one. */
  title?: string;
  /** The component draws its own heading, so the shell adds none. */
  bare?: boolean;
  /** Props for the page component (the mode pages get their mode name). */
  props?: Record<string, unknown>;
  /** Lazy, so a page's code loads only when it is opened. */
  load: Loader;
}

export interface SettingsGroup {
  /** URL part: #/settings/<id>. */
  id: string;
  label: string;
  /** Admins only. Without it everyone (operators and viewers too) can open the group. */
  admin?: boolean;
  /** Tabbed group: a tab bar, one page per tab. The first visible tab opens when the URL names none. */
  tabs?: SettingsTab[];
  /** Single-page group: these components, stacked in this order. */
  pages?: Loader[];
}

export const groups: SettingsGroup[] = [
  { id: 'station', label: 'Station', admin: true, tabs: [
    { id: 'station', label: 'Station', title: 'Station', bare: true, load: () => import('./Station.svelte') },
    { id: 'radio', label: 'Radio', bare: true, load: () => import('./RadioTune.svelte') },
  ] },
  { id: 'modes', label: 'Modes', admin: true, tabs: [
    ...MODE_TABS.map(([id, label]) => ({ id, label, bare: true, props: { mode: id }, load: () => import('./ModePage.svelte') })),
    { id: 'lookup', label: 'Caller lookup', bare: true, load: () => import('./CallerLookup.svelte') },
  ] },
  { id: 'access', label: 'Access', admin: true, pages: [() => import('./Users.svelte'), () => import('../PublicSettings.svelte')] },
  { id: 'system', label: 'System', admin: true, tabs: [
    { id: 'overview', label: 'Overview', title: 'Host and time', load: () => import('./Overview.svelte') },
    { id: 'updates', label: 'Updates', bare: true, load: () => import('./Updates.svelte') },
    { id: 'network', label: 'Network', load: () => import('./Network.svelte') },
    { id: 'wifi', label: 'WiFi', load: () => import('./Wifi.svelte') },
    { id: 'web-port', label: 'Web port', title: 'Web port of nodespot', load: () => import('./WebPort.svelte') },
    { id: 'node', label: 'Node service', load: () => import('./NodeService.svelte') },
    { id: 'terminal', label: 'Terminal', load: () => import('./Terminal.svelte') },
  ] },
  { id: 'maintenance', label: 'Maintenance', admin: true, tabs: [
    { id: 'backup', label: 'Backup & restore', load: () => import('../Backup.svelte') },
    { id: 'logs', label: 'Logs & services', load: () => import('./LogsServices.svelte') },
    { id: 'diagnostics', label: 'Diagnostics', load: () => import('./Diagnostics.svelte') },
    { id: 'audit', label: 'Audit log', load: () => import('./AuditLog.svelte') },
  ] },
  { id: 'about', label: 'About', pages: [() => import('./About.svelte')] },
  { id: 'account', label: 'Account', pages: [() => import('./Account.svelte')] },
];

/** Where "#/settings" lands: Station for an admin, Account for everyone else. */
export const defaultFor = (admin: boolean): string => (admin ? 'station' : 'account');

/** The groups a viewer may open. */
export function visibleGroups(list: readonly SettingsGroup[], admin: boolean): SettingsGroup[] {
  return list.filter((g) => admin || !g.admin);
}

/** A group's tabs. */
export function visibleTabs(g: SettingsGroup): SettingsTab[] {
  return g.tabs ?? [];
}
