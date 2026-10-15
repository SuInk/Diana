export interface CookieSite {
  domain: string;
  count: number;
  expired: number;
  youtube: boolean;
}

// Local draft summary only: never returns cookie names/values or makes requests.
export function cookieSites(content: string, now = Date.now() / 1000): CookieSite[] {
  const sites = new Map<string, CookieSite>();
  for (let line of content.split(/\r\n|\n|\r/)) {
    if (line.startsWith("#HttpOnly_")) line = line.slice(10);
    else if (line.startsWith("#")) continue;
    const fields = line.split("\t");
    if (fields.length !== 7 || !["TRUE", "FALSE"].includes(fields[1]!) || !["TRUE", "FALSE"].includes(fields[3]!) || !fields[2]!.startsWith("/") || !fields[5] || !/^\d+$/.test(fields[4]!)) continue;
    const domain = fields[0]!.toLowerCase().replace(/^\./, "");
    if (!/^(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/.test(domain)) continue;
    const site = sites.get(domain) ?? { domain, count: 0, expired: 0, youtube: domain === "youtube.com" || domain.endsWith(".youtube.com") };
    site.count++;
    const expiry = Number(fields[4]);
    if (expiry > 0 && expiry <= now) site.expired++;
    sites.set(domain, site);
  }
  return [...sites.values()].sort((a, b) => a.domain.localeCompare(b.domain));
}
