/** The browser tab title: "<CALLSIGN> · <Page>", just "<CALLSIGN>" on the dashboard (empty page), and "nodespot" in place of a missing callsign. */
export function pageTitle(callsign: string | undefined | null, page = ''): string {
  const who = (callsign ?? '').trim().toUpperCase() || 'nodespot';
  return page ? `${who} · ${page}` : who;
}
