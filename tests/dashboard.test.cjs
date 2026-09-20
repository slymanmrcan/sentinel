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

test('root and block storage render separately with available space', () => {
    const d = dashboard();
    d.run(`renderRealtime({ ts: new Date().toISOString(), filesystems: ${JSON.stringify(disks)} });`);
    const rows = d.element('filesystemTable').children;
    assert.equal(rows.length, 2);
    assert.equal(rows[0].children[0].textContent, '/');
    assert.equal(rows[1].children[0].textContent, '/mnt/block');
    assert.equal(rows[1].children[0].children[0].textContent, '/dev/sdb1');
    assert.equal(rows[1].children[2].textContent, '96.00 GB');
    assert.equal(rows[1].children[4].textContent, '27.00 GB');
    assert.equal(rows[1].children[5].textContent, '71.0%');
});

test('unavailable and stale disks never render as empty healthy storage', () => {
    const d = dashboard();
    d.run(`renderFilesystems(${JSON.stringify([disks[0], { ...disks[1], stats_available: false }])}, false, true);`);
    let rows = d.element('filesystemTable').children;
    assert.equal(rows[0].children[2].textContent, '48.00 GB');
    assert.equal(rows[1].children[2].textContent, '—');
    assert.equal(rows[1].dataset.state, 'unavailable');
    d.run(`renderRealtime({ts:new Date(Date.now()-300_000).toISOString(), filesystems:${JSON.stringify(disks)}});`);
    rows = d.element('filesystemTable').children;
    assert.equal(rows[0].children[5].textContent, 'Stale');
    assert.equal(rows[1].children[2].textContent, '—');
});

test('critical block storage affects overall health and unmounted disks disappear', () => {
    const d = dashboard();
    d.run(`renderRealtime({ ts:new Date().toISOString(), filesystems:${JSON.stringify([{ ...disks[1], used_percent: 95 }])} });`);
    assert.equal(d.element('.health-chip').dataset.state, 'critical');
    d.run(`renderRealtime({ ts:new Date().toISOString(), filesystems:${JSON.stringify([disks[0]])} });`);
    assert.equal(d.element('filesystemTable').children.length, 1);
    assert.equal(d.element('filesystemTable').children[0].children[0].textContent, '/');
});
