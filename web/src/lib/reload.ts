/** Key (sessionStorage) holding the last version this tab already reloaded for. */
export const RELOADED_KEY = 'nodespot-reloaded';

/**
 * Whether a stale tab must reload when the SSE hello reports `next`.
 * `known` is the version from an earlier hello of this page load ('' on the
 * first one); `reloadedFor` is the version this tab already reloaded for, so a
 * server that keeps reporting it cannot cause a reload loop.
 */
export function shouldReloadForVersion(known: string, next: string, reloadedFor: string | null): boolean {
  if (!known || !next || known === next) return false;
  return reloadedFor !== next;
}
