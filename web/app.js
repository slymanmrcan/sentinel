const state = {
    csrfToken: '',
    user: null,
    chart: null,
    chartRange: '1h',
    latestMetric: null,
    logSearchTimer: null,
    containerRefreshTimer: null,
    containerIntervalSeconds: 15
};

document.addEventListener('DOMContentLoaded', async () => {
    bindUI();
    const ready = await loadSession();
    if (!ready) return;

    await Promise.all([
        loadRealtime(),
        loadHistory(),
        loadSystemDetails(),
        loadContainers(),
        loadAnalysis(),
        loadAlerts(),
        loadEvents()
    ]);

    window.setInterval(loadRealtime, 2000);
    window.setInterval(loadSystemDetails, 15000);
    window.setInterval(loadAnalysis, 30000);
    window.setInterval(loadAlerts, 30000);
    window.setInterval(loadEvents, 10000);
});

function bindUI() {
    document.querySelectorAll('[data-range]').forEach((button) => {
        button.addEventListener('click', () => {
            state.chartRange = button.dataset.range;
            document.querySelectorAll('[data-range]').forEach((item) => {
                item.classList.toggle('active', item === button);
            });
            loadHistory();
        });
    });
    document.querySelectorAll('[data-series]').forEach((button) => {
        button.addEventListener('click', () => toggleSeries(button));
    });

    const menuButton = document.getElementById('menuButton');
    menuButton.addEventListener('click', () => {
        const open = document.body.classList.toggle('sidebar-open');
        menuButton.setAttribute('aria-expanded', String(open));
    });
    document.querySelectorAll('.nav-item').forEach((item) => {
        item.addEventListener('click', () => {
            document.body.classList.remove('sidebar-open');
            menuButton.setAttribute('aria-expanded', 'false');
        });
    });

    const search = document.getElementById('eventSearch');
    search.addEventListener('input', () => {
        window.clearTimeout(state.logSearchTimer);
        state.logSearchTimer = window.setTimeout(loadEvents, 250);
    });
    document.getElementById('eventLevel').addEventListener('change', loadEvents);
    document.getElementById('containerInterval').addEventListener('change', changeContainerInterval);
    document.getElementById('clearEventsButton').addEventListener('click', clearEvents);
    document.getElementById('signOutButton').addEventListener('click', signOut);
    document.getElementById('accountButton').addEventListener('click', () => {
        document.getElementById('passwordMessage').textContent = '';
        document.getElementById('accountDialog').showModal();
    });
    document.getElementById('passwordForm').addEventListener('submit', changePassword);

    if ('IntersectionObserver' in window) {
        const observer = new IntersectionObserver((entries) => {
            entries.forEach((entry) => {
                if (!entry.isIntersecting) return;
                document.querySelectorAll('.nav-item').forEach((item) => {
                    item.classList.toggle('active', item.dataset.section === entry.target.id);
                });
            });
        }, { rootMargin: '-25% 0px -65% 0px' });
        document.querySelectorAll('.dashboard-section').forEach((section) => observer.observe(section));
    }
}

async function loadSession() {
    try {
        const response = await fetch('/api/auth/me', {
            headers: { Accept: 'application/json' },
            cache: 'no-store'
        });
        if (response.status === 401) {
            window.location.replace('/login');
            return false;
        }
        if (!response.ok) throw new Error(`Session returned ${response.status}`);
        const payload = await response.json();
        state.csrfToken = payload.csrf_token;
        state.user = payload.user;
        renderUser(payload.user);
        return true;
    } catch (error) {
        console.error('Unable to load session:', error);
        window.location.replace('/login');
        return false;
    }
}

async function apiFetch(url, options = {}) {
    const request = { ...options };
    request.headers = {
        Accept: 'application/json',
        ...(options.headers || {})
    };
    const method = (request.method || 'GET').toUpperCase();
    if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) {
        request.headers['X-CSRF-Token'] = state.csrfToken;
    }
    request.cache = 'no-store';
    const response = await fetch(url, request);
    if (response.status === 401) {
        window.location.replace('/login');
        throw new Error('Session expired');
    }
    return response;
}

function renderUser(user) {
    const name = user?.name || 'Sentinel user';
    const login = user?.email || '—';
    setText('userName', name);
    setText('userLogin', login);
    const initials = name.split(/\s+/).filter(Boolean).slice(0, 2).map((part) => part[0]).join('').toUpperCase();
    setText('userInitials', initials || 'SU');
}

async function loadRealtime() {
    try {
        const response = await apiFetch('/api/metrics/realtime');
        if (!response.ok) throw new Error(`Realtime returned ${response.status}`);
        const metric = await response.json();
        state.latestMetric = metric;
        renderRealtime(metric);
        appendLivePoint(metric);
    } catch (error) {
        console.error('Unable to load realtime metrics:', error);
        setHealth('offline', 'Disconnected');
        setText('updatedAt', 'metrics unavailable');
    }
}

function renderRealtime(metric) {
    const unavailable = new Set(metric.unavailable || []);
    updatePercentMetric('cpu', metric.cpu_percent, unavailable.has('cpu'));
    updatePercentMetric('memory', metric.ram_percent, unavailable.has('memory'));
    updatePercentMetric('disk', metric.disk_percent, unavailable.has('disk'));
    updatePercentMetric('swap', metric.swap_percent, unavailable.has('swap'));

    setText('cpuCores', metric.cpu_cores || '—');
    setText('cpuTemp', unavailable.has('cpu_temp') ? 'unavailable' : `${Number(metric.cpu_temp).toFixed(1)}°C`);
    setText('memoryUsed', unavailable.has('memory') ? '—' : formatBytes(metric.ram_used));
    setText('memoryTotal', unavailable.has('memory') ? '—' : formatBytes(metric.ram_total));
    setText('diskUsed', unavailable.has('disk') ? '—' : formatBytes(metric.disk_used));
    setText('diskTotal', unavailable.has('disk') ? '—' : formatBytes(metric.disk_total));
    setText('swapUsed', unavailable.has('swap') ? '—' : formatBytes(metric.swap_used));
    setText('swapTotal', unavailable.has('swap') ? '—' : formatBytes(metric.swap_total));

    const loads = [metric.load_1, metric.load_5, metric.load_15].map((value) => Number(value || 0).toFixed(2));
    setText('loadValue', unavailable.has('load') ? '—' : loads.join(' · '));
    const cores = Math.max(1, Number(metric.cpu_cores) || 1);
    const loadPercent = clamp((Number(metric.load_1) / cores) * 100);
    setWidth('loadBar', loadPercent);
    setText('loadHint', unavailable.has('load') ? 'unavailable' : `per core ${(Number(metric.load_1) / cores).toFixed(2)}`);

    const netRxTotal = Number(metric.net_rx_total) || 0;
    const netTxTotal = Number(metric.net_tx_total) || 0;
    setText('networkTotal', unavailable.has('network') ? '—' : formatBytes(netRxTotal + netTxTotal));
    setText('networkInTotal', unavailable.has('network') ? '—' : formatBytes(netRxTotal));
    setText('networkOutTotal', unavailable.has('network') ? '—' : formatBytes(netTxTotal));
    setText('networkInRate', unavailable.has('network') ? '—' : formatRate(metric.net_rx_bps));
    setText('networkOutRate', unavailable.has('network') ? '—' : formatRate(metric.net_tx_bps));
    setWidth('networkBar', logarithmicWidth((metric.net_rx_bps || 0) + (metric.net_tx_bps || 0)));
    setText('diskReadValue', unavailable.has('disk_io') ? '—' : formatRate(metric.disk_read_bps));
    setText('diskWriteValue', unavailable.has('disk_io') ? '—' : formatRate(metric.disk_write_bps));
    setWidth('diskIOBar', logarithmicWidth((metric.disk_read_bps || 0) + (metric.disk_write_bps || 0)));

    setText('cpuModel', metric.cpu_model || '—');
    setText('osName', metric.os || '—');
    setText('uptime', formatUptime(metric.uptime));
    setText('processCount', metric.processes ?? '—');
    setText('agentName', `agent · ${metric.host_name || 'unknown'}`);
    setText('updatedAt', `updated ${new Date(metric.ts || Date.now()).toLocaleTimeString()}`);

    const peak = Math.max(
        Number(metric.cpu_percent) || 0,
        Number(metric.ram_percent) || 0,
        Number(metric.disk_percent) || 0,
        Number(metric.swap_percent) || 0
    );
    if (peak > 90) setHealth('critical', 'Critical');
    else if (peak > 75) setHealth('warning', 'Watch');
    else if (unavailable.size > 0) setHealth('warning', 'Partial data');
    else setHealth('healthy', 'Healthy');
}

function updatePercentMetric(prefix, rawValue, unavailable = false) {
    const value = clamp(rawValue);
    if (unavailable) {
        const card = document.getElementById(`${prefix}Card`);
        card.dataset.state = 'unavailable';
        setText(`${prefix}Value`, '—');
        setText(`${prefix}State`, 'Unavailable');
        setWidth(`${prefix}Bar`, 0);
        return;
    }
    const status = value > 90 ? 'critical' : value > 75 ? 'warning' : 'healthy';
    const card = document.getElementById(`${prefix}Card`);
    card.dataset.state = status;
    setText(`${prefix}Value`, `${value.toFixed(1)}%`);
    setText(`${prefix}State`, status === 'healthy' ? 'Normal' : status === 'warning' ? 'Watch' : 'Critical');
    setWidth(`${prefix}Bar`, value);
}

function setHealth(status, label) {
    const chip = document.querySelector('.health-chip');
    chip.dataset.state = status;
    setText('healthText', label);
}

function logarithmicWidth(value) {
    if (!Number.isFinite(Number(value)) || Number(value) <= 0) return 0;
    return clamp(Math.log10(Number(value) + 1) * 15);
}

async function loadHistory() {
    try {
        const response = await apiFetch(`/api/metrics/history?range=${encodeURIComponent(state.chartRange)}`);
        if (!response.ok) throw new Error(`History returned ${response.status}`);
        const metrics = await response.json();
        setText('sampleCount', `${metrics.length} samples · ${state.chartRange} window`);
        renderChart(metrics);
    } catch (error) {
        console.error('Unable to load metric history:', error);
    }
}

function renderChart(metrics) {
    const labels = metrics.map((metric) => formatChartTime(metric.ts));
    const datasets = [
        chartDataset('CPU', '#16d9e5', metrics.map((metric) => clamp(metric.cpu_percent)), 'percent', false),
        chartDataset('RAM', '#a84df1', metrics.map((metric) => clamp(metric.ram_percent)), 'percent', false),
        chartDataset('Disk', '#697070', metrics.map((metric) => clamp(metric.disk_percent)), 'percent', true),
        chartDataset('Swap', '#f05b68', metrics.map((metric) => clamp(metric.swap_percent)), 'percent', false),
        chartDataset('Net in', '#28d78c', metrics.map((metric) => Number(metric.net_rx_bps) || 0), 'bytes', true),
        chartDataset('Net out', '#d2a546', metrics.map((metric) => Number(metric.net_tx_bps) || 0), 'bytes', true)
    ];

    if (state.chart) {
        const hidden = state.chart.data.datasets.map((dataset) => dataset.hidden);
        state.chart.data.labels = labels;
        state.chart.data.datasets = datasets;
        state.chart.data.datasets.forEach((dataset, index) => { dataset.hidden = hidden[index]; });
        state.chart.update();
        return;
    }

    const canvas = document.getElementById('metricsChart');
    state.chart = new Chart(canvas.getContext('2d'), {
        type: 'line',
        data: { labels, datasets },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            animation: { duration: 220 },
            interaction: { intersect: false, mode: 'index' },
            plugins: {
                legend: { display: false },
                tooltip: {
                    backgroundColor: '#111313',
                    borderColor: '#323737',
                    borderWidth: 1,
                    titleColor: '#eceeee',
                    bodyColor: '#aeb3b3',
                    padding: 9,
                    callbacks: {
                        label: (context) => context.dataset.yAxisID === 'bytes'
                            ? `${context.dataset.label}: ${formatRate(context.raw)}`
                            : `${context.dataset.label}: ${Number(context.raw).toFixed(1)}%`
                    }
                }
            },
            scales: {
                x: {
                    border: { display: false },
                    grid: { display: false },
                    ticks: { color: '#555b5b', maxTicksLimit: 8, maxRotation: 0, font: { family: 'monospace', size: 8 } }
                },
                percent: {
                    position: 'left',
                    min: 0,
                    max: 100,
                    border: { display: false },
                    grid: { color: 'rgba(113,119,119,.10)' },
                    ticks: { color: '#555b5b', stepSize: 25, callback: (value) => `${value}%`, font: { family: 'monospace', size: 8 } }
                },
                bytes: {
                    position: 'right',
                    display: false,
                    beginAtZero: true,
                    border: { display: false },
                    grid: { display: false }
                }
            }
        }
    });
}

function chartDataset(label, color, data, axis, hidden) {
    return {
        label,
        data,
        yAxisID: axis,
        borderColor: color,
        backgroundColor: color,
        borderWidth: 1.5,
        pointRadius: 0,
        pointHoverRadius: 3,
        tension: .28,
        fill: false,
        hidden
    };
}

function toggleSeries(button) {
    if (!state.chart) return;
    const index = Number(button.dataset.series);
    const dataset = state.chart.data.datasets[index];
    dataset.hidden = !dataset.hidden;
    button.classList.toggle('active', !dataset.hidden);
    state.chart.options.scales.bytes.display = state.chart.data.datasets
        .slice(4)
        .some((item) => !item.hidden);
    state.chart.update();
}

function appendLivePoint(metric) {
    if (!state.chart || state.chartRange !== '1h' || !metric.ts) return;
    const label = formatChartTime(metric.ts);
    const labels = state.chart.data.labels;
    if (labels.at(-1) === label) return;
    labels.push(label);
    const values = [
        metric.cpu_percent,
        metric.ram_percent,
        metric.disk_percent,
        metric.swap_percent,
        metric.net_rx_bps,
        metric.net_tx_bps
    ];
    state.chart.data.datasets.forEach((dataset, index) => dataset.data.push(Number(values[index]) || 0));
    if (labels.length > 360) {
        labels.shift();
        state.chart.data.datasets.forEach((dataset) => dataset.data.shift());
    }
    state.chart.update('none');
}

async function loadSystemDetails() {
    try {
        const response = await apiFetch('/api/system/details');
        if (!response.ok) throw new Error(`System details returned ${response.status}`);
        const details = await response.json();
        setText('kernelVersion', details.kernel_version || '—');
        const status = document.getElementById('systemState');
        status.textContent = details.reboot_required ? 'Restart required' : 'Nominal';
        status.classList.toggle('warning', Boolean(details.reboot_required));
        renderProcessTable(details.processes || []);
        renderPortTable(details.listening_ports || []);
    } catch (error) {
        console.error('Unable to load system details:', error);
    }
}

function renderProcessTable(processes) {
    const body = document.getElementById('processTable');
    if (!processes.length) {
        body.replaceChildren(emptyTableRow(4, 'No process data available.'));
        return;
    }
    const fragment = document.createDocumentFragment();
    processes.forEach((process) => {
        const row = document.createElement('tr');
        row.append(
            cell(process.pid),
            cell(process.name || 'Unknown', process.command || ''),
            cell(`${Number(process.cpu || 0).toFixed(1)}%`),
            cell(`${Number(process.memory || 0).toFixed(1)}%`)
        );
        fragment.appendChild(row);
    });
    body.replaceChildren(fragment);
}

function renderPortTable(ports) {
    const body = document.getElementById('portTable');
    if (!ports.length) {
        body.replaceChildren(emptyTableRow(3, 'No listening ports returned.'));
        return;
    }
    const fragment = document.createDocumentFragment();
    ports.forEach((port) => {
        const row = document.createElement('tr');
        row.append(cell(`:${port.port}`), cell(port.name || 'Unknown'), cell(port.pid || '—'));
        fragment.appendChild(row);
    });
    body.replaceChildren(fragment);
}

async function loadContainers() {
    try {
        const response = await apiFetch('/api/containers');
        if (!response.ok) throw new Error(`Containers returned ${response.status}`);
        acceptContainerSnapshot(await response.json());
    } catch (error) {
        console.error('Unable to load container metrics:', error);
        setText('containerStatus', 'Container telemetry unavailable');
        setText('containerCollectionState', 'collection failed');
        renderContainerRows([]);
        scheduleContainerLoad(state.containerIntervalSeconds);
    }
}

function acceptContainerSnapshot(snapshot) {
    const seconds = [15, 30, 45, 60, 120].includes(Number(snapshot.interval_seconds))
        ? Number(snapshot.interval_seconds)
        : 15;
    state.containerIntervalSeconds = seconds;
    const control = document.getElementById('containerInterval');
    control.value = String(seconds);
    control.disabled = !snapshot.enabled;
    renderContainers(snapshot);
    scheduleContainerLoad(seconds);
}

function scheduleContainerLoad(seconds) {
    window.clearTimeout(state.containerRefreshTimer);
    state.containerRefreshTimer = window.setTimeout(loadContainers, Math.max(15, Number(seconds) || 15) * 1000);
}

async function changeContainerInterval(event) {
    const control = event.currentTarget;
    const previous = state.containerIntervalSeconds;
    const seconds = Number(control.value);
    control.disabled = true;
    try {
        const response = await apiFetch('/api/containers/settings', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ interval_seconds: seconds })
        });
        if (!response.ok) throw new Error(`Container settings returned ${response.status}`);
        acceptContainerSnapshot(await response.json());
    } catch (error) {
        console.error('Unable to update container interval:', error);
        control.value = String(previous);
        control.disabled = false;
    }
}

function renderContainers(snapshot) {
    const containers = snapshot.containers || [];
    const interval = formatContainerInterval(snapshot.interval_seconds);
    if (!snapshot.enabled) {
        setText('containerCollectionState', 'optional Docker telemetry · disabled');
        setText('containerStatus', snapshot.message || 'Enable container metrics in configuration');
        renderContainerSummary(null);
        renderContainerRows([], 'Container metrics are disabled. See the setup guide to enable them safely.');
        return;
    }
    if (!snapshot.available) {
        setText('containerCollectionState', `Docker telemetry · ${interval} collection · unavailable`);
        setText('containerStatus', snapshot.message || 'Docker metrics are unavailable');
        renderContainerSummary(null);
        renderContainerRows([], 'Docker metrics are enabled but unavailable. Check the configured API or socket access.');
        return;
    }
    const updated = snapshot.collected_at ? new Date(snapshot.collected_at).toLocaleTimeString() : 'just now';
    setText('containerCollectionState', `Docker telemetry · ${interval} collection · updated ${updated}`);
    setText('containerStatus', containers.length ? `${containers.length} running` : 'No running containers');
    renderContainerSummary(containers);
    renderContainerRows(containers);
}

function formatContainerInterval(seconds) {
    const value = Number(seconds) || 15;
    if (value >= 60) return `${value / 60}m`;
    return `${value}s`;
}

function renderContainerSummary(containers) {
    if (!containers) {
        ['containerCount', 'containerCPU', 'containerMemory', 'containerNetwork'].forEach((id) => setText(id, '—'));
        return;
    }
    const totals = containers.reduce((result, container) => {
        result.cpu += Number(container.cpu_percent) || 0;
        result.memory += Number(container.memory_used) || 0;
        result.rx += Number(container.net_rx_bytes) || 0;
        result.tx += Number(container.net_tx_bytes) || 0;
        return result;
    }, { cpu: 0, memory: 0, rx: 0, tx: 0 });
    setText('containerCount', containers.length);
    setText('containerCPU', `${totals.cpu.toFixed(1)}%`);
    setText('containerMemory', formatBytes(totals.memory));
    setText('containerNetwork', `${formatBytes(totals.rx)} / ${formatBytes(totals.tx)}`);
}

function renderContainerRows(containers, emptyMessage = 'No running containers returned.') {
    const body = document.getElementById('containerTable');
    if (!containers.length) {
        body.replaceChildren(emptyTableRow(6, emptyMessage));
        return;
    }
    const fragment = document.createDocumentFragment();
    containers.forEach((container) => {
        const row = document.createElement('tr');
        const name = cell(container.name || container.id, container.image || '');
        name.className = 'container-name';
        row.append(
            name,
            cell(container.status || container.state || '—'),
            cell(formatPercent(container.cpu_percent)),
            cell(`${formatBytes(container.memory_used)} / ${formatBytes(container.memory_limit)} (${formatPercent(container.memory_percent)})`),
            cell(`${formatBytes(container.net_rx_bytes)} / ${formatBytes(container.net_tx_bytes)}`),
            cell(container.pids ?? '—')
        );
        fragment.appendChild(row);
    });
    body.replaceChildren(fragment);
}

async function loadAnalysis() {
    try {
        const [anomalyResponse, baselineResponse] = await Promise.all([
            apiFetch('/api/anomalies'),
            apiFetch('/api/metrics/summary')
        ]);
        if (!anomalyResponse.ok || !baselineResponse.ok) throw new Error('Analysis endpoints failed');
        const anomalies = await anomalyResponse.json();
        const summary = await baselineResponse.json();
        renderAnomalies(anomalies);
        renderBaselines(summary.baselines || []);
    } catch (error) {
        console.error('Unable to load analysis:', error);
    }
}

function renderAnomalies(anomalies) {
    setText('anomalyCount', anomalies.length);
    const list = document.getElementById('anomalyList');
    if (!anomalies.length) {
        list.replaceChildren(emptyState('No anomalies detected in the current baseline.'));
        return;
    }
    const fragment = document.createDocumentFragment();
    anomalies.forEach((anomaly) => {
        fragment.appendChild(analysisEvent(
            anomaly.ts,
            anomaly.message,
            `${anomaly.metric} · baseline ${Number(anomaly.baseline_mean).toFixed(2)}`,
            `${Number(anomaly.z_score).toFixed(1)}σ`,
            anomaly.severity
        ));
    });
    list.replaceChildren(fragment);
}

function renderBaselines(baselines) {
    const body = document.getElementById('baselineTable');
    if (!baselines.length) {
        body.replaceChildren(emptyTableRow(4, 'No baseline samples yet.'));
        return;
    }
    const fragment = document.createDocumentFragment();
    let minimumSamples = Number.MAX_SAFE_INTEGER;
    baselines.forEach((baseline) => {
        minimumSamples = Math.min(minimumSamples, baseline.count);
        const row = document.createElement('tr');
        row.append(
            cell(String(baseline.metric).toUpperCase()),
            cell(Number(baseline.mean).toFixed(2)),
            cell(Number(baseline.stddev).toFixed(2)),
            cell(baseline.count)
        );
        fragment.appendChild(row);
    });
    body.replaceChildren(fragment);
    setText('baselineState', minimumSamples >= 12 ? 'ready' : `building · ${minimumSamples}/12`);
}

async function loadAlerts() {
    try {
        const [rulesResponse, eventsResponse] = await Promise.all([
            apiFetch('/api/alerts/rules'),
            apiFetch('/api/alerts/events')
        ]);
        if (!rulesResponse.ok || !eventsResponse.ok) throw new Error('Alert endpoints failed');
        const rules = await rulesResponse.json();
        const events = await eventsResponse.json();
        renderRules(rules);
        renderAlerts(events);
    } catch (error) {
        console.error('Unable to load alerts:', error);
    }
}

function renderRules(rules) {
    const container = document.getElementById('alertRules');
    if (!rules.length) {
        container.replaceChildren(emptyState('No alert rules configured.'));
        return;
    }
    const fragment = document.createDocumentFragment();
    rules.forEach((rule) => {
        const card = document.createElement('article');
        card.className = 'rule-card';
        const name = document.createElement('strong');
        name.textContent = rule.name;
        const threshold = document.createElement('b');
        threshold.textContent = `>${Number(rule.threshold).toFixed(0)}%`;
        const detail = document.createElement('span');
        detail.textContent = `${rule.metric} · ${rule.severity} · ${rule.enabled ? 'enabled' : 'disabled'}`;
        card.append(name, threshold, detail);
        fragment.appendChild(card);
    });
    container.replaceChildren(fragment);
}

function renderAlerts(events) {
    setText('alertCount', events.length);
    setText('alertNavCount', events.length);
    const list = document.getElementById('alertList');
    if (!events.length) {
        list.replaceChildren(emptyState('No alert events in the retention window.'));
        return;
    }
    const fragment = document.createDocumentFragment();
    events.forEach((event) => {
        fragment.appendChild(analysisEvent(
            event.ts, event.rule_name, event.message,
            `${Number(event.value).toFixed(1)}%`, event.severity
        ));
    });
    list.replaceChildren(fragment);
}

function analysisEvent(timestamp, title, detail, score, severity) {
    const row = document.createElement('div');
    row.className = `event-row ${severity || ''}`;
    const time = document.createElement('time');
    time.className = 'event-time';
    time.textContent = formatEventTime(timestamp);
    const copy = document.createElement('div');
    copy.className = 'event-copy';
    const strong = document.createElement('strong');
    strong.textContent = String(title || '');
    const paragraph = document.createElement('p');
    paragraph.textContent = String(detail || '');
    copy.append(strong, paragraph);
    const value = document.createElement('span');
    value.className = 'event-score';
    value.textContent = score;
    row.append(time, copy, value);
    return row;
}

async function loadEvents() {
    const level = document.getElementById('eventLevel').value;
    const query = document.getElementById('eventSearch').value;
    try {
        const response = await apiFetch(`/api/logs?level=${encodeURIComponent(level)}&query=${encodeURIComponent(query)}`);
        if (!response.ok) throw new Error(`Events returned ${response.status}`);
        const events = await response.json();
        renderEvents(events);
    } catch (error) {
        console.error('Unable to load events:', error);
    }
}

function renderEvents(events) {
    setText('eventCount', events.length);
    const list = document.getElementById('eventList');
    if (!events.length) {
        list.replaceChildren(emptyState('No events match the current filters.'));
        return;
    }
    const fragment = document.createDocumentFragment();
    events.forEach((event) => {
        const row = document.createElement('div');
        row.className = `event-row ${String(event.level || '').toLowerCase()}`;
        const time = document.createElement('time');
        time.className = 'event-time';
        time.textContent = formatEventTime(event.ts);
        const level = document.createElement('span');
        level.className = 'event-level';
        level.textContent = event.level || 'INFO';
        const source = document.createElement('span');
        source.className = 'event-source';
        source.textContent = event.source || 'system';
        const message = document.createElement('span');
        message.className = 'event-message';
        message.textContent = event.message || '';
        row.append(time, level, source, message);
        fragment.appendChild(row);
    });
    list.replaceChildren(fragment);
}

async function clearEvents() {
    if (!window.confirm('Clear all Sentinel events? A new audit event will be created.')) return;
    try {
        const response = await apiFetch('/api/logs', { method: 'DELETE' });
        if (!response.ok) throw new Error(`Clear returned ${response.status}`);
        await loadEvents();
    } catch (error) {
        console.error('Unable to clear events:', error);
    }
}

async function signOut() {
    try {
        await apiFetch('/api/auth/logout', { method: 'POST' });
    } finally {
        window.location.replace('/login');
    }
}

async function changePassword(event) {
    event.preventDefault();
    const message = document.getElementById('passwordMessage');
    const button = document.getElementById('savePasswordButton');
    message.textContent = '';
    button.disabled = true;
    try {
        const response = await apiFetch('/api/auth/password', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                current_password: document.getElementById('currentPassword').value,
                new_password: document.getElementById('newPassword').value
            })
        });
        const payload = await response.json();
        if (!response.ok) {
            message.textContent = payload.error || 'Password update failed.';
            return;
        }
        window.location.replace('/login');
    } catch (error) {
        message.textContent = 'Password update failed.';
        console.error(error);
    } finally {
        button.disabled = false;
    }
}

function setText(id, value) {
    const element = document.getElementById(id);
    if (element) element.textContent = String(value);
}

function setWidth(id, value) {
    const element = document.getElementById(id);
    if (element) element.style.width = `${clamp(value)}%`;
}

function clamp(value) {
    const numeric = Number(value);
    if (!Number.isFinite(numeric)) return 0;
    return Math.min(100, Math.max(0, numeric));
}

function formatPercent(value) {
    return `${clamp(value).toFixed(1)}%`;
}

function formatBytes(value) {
    const bytes = Number(value) || 0;
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    if (bytes <= 0) return '0 B';
    const index = Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)));
    const result = bytes / (1024 ** index);
    return `${result.toFixed(index < 2 ? 0 : 2)} ${units[index]}`;
}

function formatRate(value) {
    return `${formatBytes(value)}/s`;
}

function formatUptime(seconds) {
    const total = Number(seconds) || 0;
    const days = Math.floor(total / 86400);
    const hours = Math.floor((total % 86400) / 3600);
    const minutes = Math.floor((total % 3600) / 60);
    return `${days}d ${hours}h ${minutes}m`;
}

function formatChartTime(timestamp) {
    const date = new Date(timestamp);
    if (state.chartRange === '7d') {
        return date.toLocaleDateString([], { month: 'short', day: 'numeric', hour: '2-digit' });
    }
    return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

function formatEventTime(timestamp) {
    return new Date(timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

function cell(value, title = '') {
    const element = document.createElement('td');
    element.textContent = String(value ?? '—');
    if (title) element.title = title;
    return element;
}

function emptyTableRow(columns, message) {
    const row = document.createElement('tr');
    const element = cell(message);
    element.colSpan = columns;
    row.appendChild(element);
    return row;
}

function emptyState(message) {
    const element = document.createElement('div');
    element.className = 'empty-state';
    element.textContent = message;
    return element;
}
