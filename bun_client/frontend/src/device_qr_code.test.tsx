import { describe, expect, test } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import { DeviceQRCodeView } from './components/DeviceQRCode';

const LINKS = [
    { url: 'http://192.168.1.23:5172/?owner=o&repo=r&number=7', label: '192.168.1.23 (en0)' },
    { url: 'http://10.0.0.12:5172/?owner=o&repo=r&number=7', label: '10.0.0.12 (eth0)' },
];

describe('DeviceQRCodeView', () => {
    test('draws a QR code for the first link and shows it', () => {
        const html = renderToStaticMarkup(
            <DeviceQRCodeView links={LINKS.slice(0, 1)} loading={false} failed={false} />
        );
        expect(html).toContain('<svg');
        expect(html).toContain('<title>QR code for http://192.168.1.23:5172/?owner=o&amp;repo=r');
        expect(html).toContain('href="http://192.168.1.23:5172/?owner=o&amp;repo=r&amp;number=7"');
        // One address: nothing to choose between.
        expect(html).not.toContain('<select');
    });

    test('offers a choice when there is more than one address', () => {
        const html = renderToStaticMarkup(
            <DeviceQRCodeView links={LINKS} loading={false} failed={false} />
        );
        expect(html).toContain('<select');
        expect(html).toContain('>192.168.1.23 (en0)</option>');
        expect(html).toContain('>10.0.0.12 (eth0)</option>');
    });

    test('says why there is no code', () => {
        const none = renderToStaticMarkup(
            <DeviceQRCodeView links={[]} loading={false} failed={false} />
        );
        expect(none).not.toContain('<svg');
        expect(none).toContain('No network address to offer');

        const failed = renderToStaticMarkup(
            <DeviceQRCodeView links={[]} loading={false} failed={true} />
        );
        expect(failed).toContain('Couldn&#x27;t ask the server for its network address.');

        const loading = renderToStaticMarkup(
            <DeviceQRCodeView links={[]} loading={true} failed={false} />
        );
        expect(loading).toContain('Looking up');
    });

    test("shows the page's own address while the bridge's are on their way", () => {
        const html = renderToStaticMarkup(
            <DeviceQRCodeView links={LINKS.slice(1)} loading={true} failed={false} />
        );
        expect(html).toContain('<svg');
        expect(html).not.toContain('Looking up');
    });
});
