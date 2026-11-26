// Applies the saved theme before first paint (external file so the server's
// Content-Security-Policy can forbid inline scripts). Default is dark; only an
// explicit "light" (or "system" resolving to a light OS) turns it off.
try {
  var t = localStorage.getItem('nodespot-theme');
  if (t !== 'light' && t !== 'system') t = 'dark';
  if (t === 'dark' || matchMedia('(prefers-color-scheme: dark)').matches) {
    document.documentElement.classList.add('dark');
  }
} catch (e) {
  document.documentElement.classList.add('dark');
}
