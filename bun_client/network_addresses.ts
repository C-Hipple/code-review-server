/**
 * The addresses other devices can reach the bridge at, for the Preferences
 * modal's "open on another device" QR code (`GET /api/network`).
 *
 * A page opened at localhost can't learn the machine's LAN address itself, so
 * the bridge lists its interfaces' IPv4 addresses, the likeliest home-network
 * one first. The page then pairs them with the port the bridge listens on.
 */

import type { NetworkInterfaceInfo } from 'node:os';

/** One address another device might reach the bridge at. */
export interface LanAddress {
    address: string;
    /** The interface it's on (`en0`, `wlan0`), empty when the bind names it. */
    interface: string;
}

/** The reply of `GET /api/network`. */
export interface NetworkInfo {
    port: number;
    addresses: LanAddress[];
}

/** Hosts that mean "every interface" to Bun.serve. */
const WILDCARD_HOSTS = new Set(['', '0.0.0.0', '::']);

/** Container, VM and VPN bridges: real addresses a phone can't route to. */
const VIRTUAL_INTERFACE =
    /^(docker|br-|veth|virbr|vboxnet|vmnet|cni|flannel|podman|lxc|lxd|vethernet)/i;

export function isLoopbackHost(host: string): boolean {
    const h = host.toLowerCase().replace(/^\[|\]$/g, '');
    return h === 'localhost' || h.endsWith('.localhost') || h === '::1' || h.startsWith('127.');
}

/** Lower is likelier to be the Wi-Fi a phone shares with this machine. */
function rank(address: string, name: string): number {
    if (VIRTUAL_INTERFACE.test(name)) return 4;
    if (address.startsWith('192.168.')) return 0;
    if (address.startsWith('10.')) return 1;
    if (/^172\.(1[6-9]|2\d|3[01])\./.test(address)) return 2;
    return 3;
}

/**
 * The addresses to offer, best first. With the bridge bound to one address
 * (`CRS_HOST`) that is the only one that answers, and none does when it's
 * loopback.
 */
export function lanAddresses(
    interfaces: NodeJS.Dict<NetworkInterfaceInfo[]>,
    bindHost: string
): LanAddress[] {
    if (!WILDCARD_HOSTS.has(bindHost)) {
        return isLoopbackHost(bindHost) ? [] : [{ address: bindHost, interface: '' }];
    }
    const found: (LanAddress & { rank: number })[] = [];
    for (const [name, infos] of Object.entries(interfaces)) {
        for (const info of infos ?? []) {
            // Link-local 169.254.x is what an interface gets when DHCP failed.
            if (info.internal || info.family !== 'IPv4' || info.address.startsWith('169.254.')) {
                continue;
            }
            found.push({ address: info.address, interface: name, rank: rank(info.address, name) });
        }
    }
    // A stable sort, so equals keep the OS's interface order.
    found.sort((a, b) => a.rank - b.rank);
    return found.map(({ address, interface: name }) => ({ address, interface: name }));
}
