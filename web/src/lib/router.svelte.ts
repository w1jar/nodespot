import { movedPath } from './sysnav';

// Hash router: "#/path?query". `path` excludes the query string.
function parse(): { path: string; query: string } {
  const h = location.hash.replace(/^#/, '');
  if (!h.startsWith('/')) return { path: '/', query: '' };
  const i = h.indexOf('?');
  return i < 0 ? { path: h, query: '' } : { path: h.slice(0, i), query: h.slice(i + 1) };
}
/** Routes that moved (#/updates -> #/system/updates) are rewritten in place, so old links and bookmarks keep working. */
function redirectMoved() {
  const p = parse();
  const to = movedPath(p.path);
  if (to) location.replace('#' + to + (p.query ? (to.includes('?') ? '&' : '?') + p.query : ''));
}
redirectMoved();
export const router = $state<{ path: string; query: string }>(parse());
addEventListener('hashchange', () => { redirectMoved(); Object.assign(router, parse()); });
export function navigate(path: string) { if (parse().path !== path || parse().query) location.hash = path; else Object.assign(router, parse()); }
