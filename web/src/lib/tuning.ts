// The "tune your audio" reminder: when the dashboard shows it and where its button goes. Pure functions, tested in tuning.test.ts.
import type { NodeJSON } from './types';

/** Where "Tune now" goes: Settings > Station > Audio, step 1, with Auto-set emphasized (the `tune` flag). */
export const TUNE_HREF = '#/settings/station/audio?tune=1';

/** The reminder shows to admins only, while the node says its audio needs tuning (a radio with a receive level control that
 * has not been tuned or dismissed for this hardware). It is hidden while the node cannot be reached: the flag is then stale. */
export function showTuneBanner(role: string | undefined, node: Pick<NodeJSON, 'running'> & { radio: Pick<NodeJSON['radio'], 'present' | 'needsTuning'> } | null | undefined): boolean {
  return role === 'admin' && !!node && node.running && node.radio.present && node.radio.needsTuning === true;
}

/** True when the Audio tab was opened from the reminder: the query has the `tune` flag. */
export function wantsAutosetFocus(query: string): boolean {
  return new URLSearchParams(query).has('tune');
}
