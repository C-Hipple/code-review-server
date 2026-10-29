import { describe, expect, test } from 'bun:test';
import type { NetworkInterfaceInfo } from 'node:os';
import { isLoopbackHost, lanAddresses } from './network_addresses';

function v4(address: string, internal = false): NetworkInterfaceInfo {
    return {
        address,
        netmask: '255.255.255.0',
        family: 'IPv4',
        mac: '00:00:00:00:00:00',
        internal,
        cidr: `${address}/24`,
    };
}

function v6(address: string): NetworkInterfaceInfo {
    return {
        address,
        netmask: 'ffff:ffff:ffff:ffff::',
        family: 'IPv6',
        mac: '00:00:00:00:00:00',
        internal: false,
        cidr: `${address}/64`,
        scopeid: 0,
    };
}

describe('lanAddresses', () => {
    test('lists external IPv4 addresses, home-network ranges first', () => {
        const interfaces = {
            lo: [v4('127.0.0.1', true)],
            docker0: [v4('172.17.0.1')],
            utun3: [v4('100.101.102.103')],
            eth0: [v4('10.0.0.12'), v6('fe80::1')],
            en0: [v4('192.168.1.23'), v6('fe80::2')],
        };
        expect(lanAddresses(interfaces, '0.0.0.0')).toEqual([
            { address: '192.168.1.23', interface: 'en0' },
            { address: '10.0.0.12', interface: 'eth0' },
            { address: '100.101.102.103', interface: 'utun3' },
            // A docker bridge has a private address no phone can route to.
            { address: '172.17.0.1', interface: 'docker0' },
        ]);
    });

    test('skips link-local addresses', () => {
        expect(lanAddresses({ en5: [v4('169.254.10.2')] }, '0.0.0.0')).toEqual([]);
    });

    test('ranks the 172.16/12 private range above other addresses', () => {
        const interfaces = { eth1: [v4('8.8.4.4')], eth0: [v4('172.20.1.5')] };
        expect(lanAddresses(interfaces, '::').map(a => a.address)).toEqual([
            '172.20.1.5',
            '8.8.4.4',
        ]);
    });

    test('bound to one address, offers only that one', () => {
        const interfaces = { en0: [v4('192.168.1.23')], eth0: [v4('10.0.0.12')] };
        expect(lanAddresses(interfaces, '10.0.0.12')).toEqual([
            { address: '10.0.0.12', interface: '' },
        ]);
    });

    test('bound to loopback, offers none', () => {
        const interfaces = { en0: [v4('192.168.1.23')] };
        expect(lanAddresses(interfaces, '127.0.0.1')).toEqual([]);
        expect(lanAddresses(interfaces, 'localhost')).toEqual([]);
        expect(lanAddresses(interfaces, '::1')).toEqual([]);
    });
});

describe('isLoopbackHost', () => {
    test.each([
        ['localhost', true],
        ['LOCALHOST', true],
        ['app.localhost', true],
        ['127.0.0.1', true],
        ['127.1.2.3', true],
        ['::1', true],
        ['[::1]', true],
        ['192.168.1.23', false],
        ['mymac.local', false],
    ])('%s → %p', (host, expected) => {
        expect(isLoopbackHost(host)).toBe(expected);
    });
});
