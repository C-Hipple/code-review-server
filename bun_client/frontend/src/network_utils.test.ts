import { describe, expect, test } from 'bun:test';
import { deviceLinks, hostForUrl, isLoopbackHost, type PageLocation } from './network_utils';

function at(url: string): PageLocation {
    const { protocol, host, hostname, pathname, search } = new URL(url);
    return { protocol, host, hostname, pathname, search };
}

const INFO = {
    port: 5172,
    addresses: [
        { address: '192.168.1.23', interface: 'en0' },
        { address: '10.0.0.12', interface: 'eth0' },
    ],
};

describe('deviceLinks', () => {
    test('opened at localhost, links to each of the bridge addresses at its port', () => {
        expect(deviceLinks(at('http://localhost:5172/'), INFO)).toEqual([
            { url: 'http://192.168.1.23:5172/', label: '192.168.1.23 (en0)' },
            { url: 'http://10.0.0.12:5172/', label: '10.0.0.12 (eth0)' },
        ]);
    });

    test('carries the page being shown', () => {
        const page = 'http://127.0.0.1:5172/?owner=o&repo=r&number=7&view=ai';
        expect(deviceLinks(at(page), INFO)[0].url).toBe(
            'http://192.168.1.23:5172/?owner=o&repo=r&number=7&view=ai'
        );
    });

    test("puts the page's own address first, and doesn't repeat it", () => {
        expect(deviceLinks(at('http://10.0.0.12:5172/?owner=o'), INFO)).toEqual([
            {
                url: 'http://10.0.0.12:5172/?owner=o',
                label: "10.0.0.12:5172 (this page's address)",
            },
            { url: 'http://192.168.1.23:5172/?owner=o', label: '192.168.1.23 (en0)' },
        ]);
    });

    test('uses the bridge port, not the dev server the page came from', () => {
        expect(deviceLinks(at('http://localhost:5173/'), INFO)[0].url).toBe(
            'http://192.168.1.23:5172/'
        );
    });

    test('a hostname the page was opened at is offered before the bridge answers', () => {
        expect(deviceLinks(at('http://mymac.local:5172/'), null)).toEqual([
            { url: 'http://mymac.local:5172/', label: "mymac.local:5172 (this page's address)" },
        ]);
    });

    test('nothing to offer from localhost with no addresses', () => {
        expect(deviceLinks(at('http://localhost:5172/'), null)).toEqual([]);
        expect(deviceLinks(at('http://localhost:5172/'), { port: 5172, addresses: [] })).toEqual(
            []
        );
    });

    test('an address without an interface name is labelled by itself', () => {
        const info = { port: 8080, addresses: [{ address: 'fd00::5', interface: '' }] };
        expect(deviceLinks(at('http://[::1]:8080/'), info)).toEqual([
            { url: 'http://[fd00::5]:8080/', label: 'fd00::5' },
        ]);
    });
});

describe('hostForUrl', () => {
    test('brackets IPv6, leaves the rest', () => {
        expect(hostForUrl('fd00::5')).toBe('[fd00::5]');
        expect(hostForUrl('[fd00::5]')).toBe('[fd00::5]');
        expect(hostForUrl('192.168.1.23')).toBe('192.168.1.23');
        expect(hostForUrl('mymac.local')).toBe('mymac.local');
    });
});

describe('isLoopbackHost', () => {
    test('loopback names and addresses', () => {
        for (const host of [
            'localhost',
            'app.localhost',
            '127.0.0.1',
            '127.9.9.9',
            '[::1]',
            '::1',
        ]) {
            expect(isLoopbackHost(host)).toBe(true);
        }
        for (const host of ['192.168.1.23', '10.0.0.1', 'mymac.local', '[fd00::5]']) {
            expect(isLoopbackHost(host)).toBe(false);
        }
    });
});
