const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

function dashboard() {
    const elements = new Map();
    const node = () => ({ textContent: '', dataset: {}, style: {}, children: [], classList: { toggle() {} },
        append(...items) { this.children.push(...items); },
        appendChild(item) { this.children.push(item); },
        replaceChildren(...items) { this.children = items; }
    });
    const element = (id) => {
        if (!elements.has(id)) elements.set(id, node());
        return elements.get(id);
    };
    const context = vm.createContext({ console, Date, Set, Map, document: {
        addEventListener() {}, getElementById: element, querySelector: element, createElement: node
    }});
    vm.runInContext(fs.readFileSync(path.join(__dirname, '../web/app.js'), 'utf8'), context);
    vm.runInContext('renderContainerRows = () => {};', context);
    return { run: (code) => vm.runInContext(code, context), element };
}

test('old host samples are never labelled Healthy', () => {
    const d = dashboard();
    d.run(`renderRealtime({ts: new Date(Date.now() - 5 * 60_000).toISOString()});`);
    assert.notEqual(d.element('.health-chip').dataset.state, 'healthy');
});

test('an empty Docker inventory is not proof all workloads are healthy', () => {
    const d = dashboard();
    d.run(`renderContainers({enabled:true,available:true,collected_at:new Date().toISOString(),containers:[]});`);
    assert.notEqual(d.element('overviewContainerCard').dataset.state, 'healthy');
});

test('partial Docker stats are not labelled healthy or totalled as zero', () => {
    const d = dashboard();
    d.run(`renderContainers({enabled:true,available:true,partial:true,collected_at:new Date().toISOString(),containers:[{state:'running',stats_available:false}]});`);
    assert.equal(d.element('overviewContainerCard').dataset.state, 'warning');
    assert.equal(d.element('containerMemory').textContent, '—');
});

test('old container stats are not presented as fresh', () => {
    const d = dashboard();
    d.run(`renderContainers({enabled:true,available:true,collected_at:new Date(Date.now()-300_000).toISOString(),containers:[{state:'running',stats_available:true}]});`);
    assert.notEqual(d.element('overviewContainerCard').dataset.state, 'healthy');
    assert.equal(d.element('containerMemory').textContent, '—');
});

test('unavailable history stays a gap while a real zero remains zero', () => {
    const d = dashboard();
    assert.equal(d.run(`chartMetric({cpu_percent:0,unavailable:['cpu']},'cpu_percent','cpu')`),null);
    assert.equal(d.run(`chartMetric({cpu_percent:0},'cpu_percent','cpu')`),0);
});

const disks = [
    { device: '/dev/sda1', mountpoint: '/', fstype: 'ext4', total: 48 * 1024 ** 3, used: 21 * 1024 ** 3, available: 28 * 1024 ** 3, used_percent: 43, stats_available: true },
    { device: '/dev/sdb1', mountpoint: '/mnt/block', fstype: 'ext4', total: 96 * 1024 ** 3, used: 65 * 1024 ** 3, available: 27 * 1024 ** 3, used_percent: 71, stats_available: true }
];

function descendant(node, className) {
    if (node.className === className) return node;
    for (const child of node.children) {
        const found = descendant(child, className);
        if (found) return found;
    }
}

test('system disk and block storage have distinct cards with capacity and available space', () => {
    const d = dashboard();
    d.run(`renderRealtime({ ts: new Date().toISOString(), filesystems: ${JSON.stringify([...disks].reverse())} });`);
    const cards = d.element('filesystemCards').children;
    assert.equal(cards.length, 2);
    assert.equal(descendant(cards[0], 'storage-disk-name').textContent, 'System disk');
    assert.equal(descendant(cards[0], 'storage-mount').textContent, '/');
    assert.equal(descendant(cards[1], 'storage-disk-name').textContent, 'Block storage');
    assert.equal(descendant(cards[1], 'storage-mount').textContent, '/mnt/block');
    assert.equal(descendant(cards[1], 'storage-device').textContent, '/dev/sdb1 · ext4');
    assert.equal(descendant(cards[1], 'storage-used').textContent, '65.00 GB');
    assert.equal(descendant(cards[1], 'storage-total').textContent, 'used of 96.00 GB');
    assert.equal(descendant(cards[1], 'storage-free').textContent, '27.00 GB available');
    assert.equal(descendant(cards[1], 'storage-percent').textContent, '71.0% used');
});

test('unavailable and stale disks never render as empty healthy storage', () => {
    const d = dashboard();
    d.run(`renderFilesystems(${JSON.stringify([disks[0], { ...disks[1], stats_available: false }])}, false, true);`);
    let cards = d.element('filesystemCards').children;
    assert.equal(descendant(cards[0], 'storage-total').textContent, 'used of 48.00 GB');
    assert.equal(descendant(cards[1], 'storage-used').textContent, '—');
    assert.equal(cards[1].dataset.state, 'unavailable');
    d.run(`renderRealtime({ts:new Date(Date.now()-300_000).toISOString(), filesystems:${JSON.stringify(disks)}});`);
    cards = d.element('filesystemCards').children;
    assert.equal(descendant(cards[0], 'storage-disk-status').textContent, 'Stale data');
    assert.equal(descendant(cards[1], 'storage-used').textContent, '—');
});

test('critical block storage affects overall health and unmounted disks disappear', () => {
    const d = dashboard();
    d.run(`renderRealtime({ ts:new Date().toISOString(), filesystems:${JSON.stringify([{ ...disks[1], used_percent: 95 }])} });`);
    assert.equal(d.element('.health-chip').dataset.state, 'critical');
    assert.equal(d.element('filesystemCards').children[0].dataset.state, 'critical');
    d.run(`renderRealtime({ ts:new Date().toISOString(), filesystems:${JSON.stringify([disks[0]])} });`);
    assert.equal(d.element('filesystemCards').children.length, 1);
    assert.equal(descendant(d.element('filesystemCards').children[0], 'storage-mount').textContent, '/');
});

test('boot partitions stay in expandable details and preserve open state across refreshes', () => {
    const d = dashboard();
    const filesystems = [...disks, {...disks[0], mountpoint:'/boot'}, {...disks[0], mountpoint:'/boot/efi'}];
    d.run(`renderFilesystems(${JSON.stringify(filesystems)}, false, false);`);
    assert.equal(d.element('filesystemCards').children.length, 2);
    assert.equal(d.element('filesystemTable').children.length, 2);
    assert.equal(d.element('filesystemSystem').hidden, false);
    assert.equal(d.element('filesystemSystemCount').textContent, '(2)');
    d.element('filesystemSystem').open = true;
    d.run(`renderFilesystems(${JSON.stringify(filesystems)}, false, false);`);
    assert.equal(d.element('filesystemSystem').open, true);
    d.run(`renderFilesystems(${JSON.stringify(disks)}, false, false);`);
    assert.equal(d.element('filesystemSystem').hidden, true);
    assert.equal(d.element('filesystemTable').children.length, 0);
});

test('Telegram status shows safe delivery details and only enables admin test control', async () => {
    const d = dashboard();
    d.run(`state.user={role:'viewer'}; apiFetch=async()=>({ok:true,json:async()=>({enabled:true,pending:2,last_success:'0001-01-01T00:00:00Z',last_failure:'2026-09-21T00:00:00Z',last_error:'Telegram hız sınırı (429)',storage_error:'',dropped:3,ssh:'Kapalı'})});`);
    await d.run('loadTelegram()');
    assert.equal(d.element('telegramState').textContent, 'Telegram etkin');
    assert.match(d.element('telegramDelivery').textContent, /Bekleyen: 2/);
    assert.match(d.element('telegramHealth').textContent, /429/);
    assert.equal(d.element('telegramTest').hidden, true);
    d.run(`state.user={role:'admin'};`);
    await d.run('loadTelegram()');
    assert.equal(d.element('telegramTest').hidden, false);
});
