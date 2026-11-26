export type Theme = 'system' | 'light' | 'dark';
// Dark is the default; 'system' is stored explicitly so it can be told apart
// from "never chose".
function read(): Theme {
  try { const v = localStorage.getItem('nodespot-theme'); if (v === 'light' || v === 'dark' || v === 'system') return v; } catch { /* ignore */ }
  return 'dark';
}
export const theme = $state<{ value: Theme }>({ value: read() });
function apply() {
  const dark = theme.value === 'dark' || (theme.value === 'system' && matchMedia('(prefers-color-scheme: dark)').matches);
  document.documentElement.classList.toggle('dark', dark);
}
export function setTheme(t: Theme) {
  theme.value = t;
  try { localStorage.setItem('nodespot-theme', t); } catch { /* ignore */ }
  apply();
}
// dark -> light -> system -> dark
export function cycleTheme() { setTheme(theme.value === 'dark' ? 'light' : theme.value === 'light' ? 'system' : 'dark'); }
export function initTheme() {
  apply();
  matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => { if (theme.value === 'system') apply(); });
}
