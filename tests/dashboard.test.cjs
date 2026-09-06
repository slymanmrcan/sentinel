const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

function dashboard() {
    const elements = new Map();
    const element = (id) => {
        if (!elements.has(id)) elements.set(id, { textContent: '', dataset: {}, style: {}, classList: { toggle() {} } });
        return elements.get(id);
    };
    const context = vm.createContext({ console, Date, Set, Map, document: {
        addEventListener() {}, getElementById: element, querySelector: element
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
