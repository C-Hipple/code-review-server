/**
 * The links Preferences offers for opening the UI on another device — a phone
 * on the same Wi-Fi scans one as a QR code. The bridge lists its LAN addresses
 * (`GET /api/network`, bun_client/network_addresses.ts) because a page opened
 * at localhost can't learn them itself.
 */

/** The reply of `GET /api/network`. */
export interface NetworkInfo {
    port: number;
    addresses: { address: string; interface: string }[];
}

export interface DeviceLink {
    url: string;
    label: string;
}

/** The parts of `window.location` a link is built from. */
export type PageLocation = Pick<Location, 'protocol' | 'host' | 'hostname' | 'pathname' | 'search'>;

export function isLoopbackHost(host: string): boolean {
    const h = host.toLowerCase().replace(/^\[|\]$/g, '');
    return h === 'localhost' || h.endsWith('.localhost') || h === '::1' || h.startsWith('127.');
}

/** An address as a URL's host: an IPv6 one goes in brackets. */
export function hostForUrl(address: string): string {
    return address.includes(':') && !address.startsWith('[') ? `[${address}]` : address;
}

/**
 * Links to the page being shown, best first. The address this page was opened
 * at comes first unless it's loopback, since it's known to work; then each of
 * the bridge's, at the port it listens on.
 */
export function deviceLinks(location: PageLocation, info: NetworkInfo | null): DeviceLink[] {
    const page = `${location.pathname}${location.search}`;
    const links: DeviceLink[] = [];
    if (!isLoopbackHost(location.hostname)) {
        links.push({
            url: `${location.protocol}//${location.host}${page}`,
            label: `${location.host} (this page's address)`,
        });
    }
    for (const { address, interface: name } of info?.addresses ?? []) {
        const url = `http://${hostForUrl(address)}:${info?.port}${page}`;
        if (links.some(link => link.url === url)) continue;
        links.push({ url, label: name ? `${address} (${name})` : address });
    }
    return links;
}
