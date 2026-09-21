const pollingIntervals = {
    realtime: 30000,
    systemDetails: 60000,
    systemServices: 60000,
    analysis: 60000,
    alerts: 60000,
    events: 30000
};

const listLimits = {
    containers: 10,
    anomalies: 10,
    events: 20
};

const state = {
    csrfToken: '',
    user: null,
    chart: null,
    chartRange: '1h',
    latestMetric: null,
    logSearchTimer: null,
    containerRefreshTimer: null,
    containerIntervalSeconds: 30,
    systemServicesLoading: false,
    containers: [],
    containersExpanded: false,
    anomalies: [],
    anomaliesExpanded: false,
    events: [],
    eventsExpanded: false,
    expandedServices: new Set()
};

document.addEventListener('DOMContentLoaded', async () => {
    bindUI();
    const ready = await loadSession();
    if (!ready) return;

    await Promise.all([
        loadRealtime(),
        loadHistory(),
        loadSystemDetails(),
        loadSystemServices(),
        loadContainers(),
        loadAnalysis(),
        loadAlerts(),
        loadEvents(),
        loadTelegram()
    ]);

    window.setInterval(loadRealtime, pollingIntervals.realtime);
    window.setInterval(loadSystemDetails, pollingIntervals.systemDetails);
    window.setInterval(loadSystemServices, pollingIntervals.systemServices);
    window.setInterval(loadAnalysis, pollingIntervals.analysis);
    window.setInterval(loadAlerts, pollingIntervals.alerts);
    window.setInterval(loadEvents, pollingIntervals.events);
    window.setInterval(loadTelegram, 30000);
});

function bindUI() {
    setText('overviewRefreshState', `host metrics · ${formatInterval(pollingIntervals.realtime / 1000)} refresh`);
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
        state.eventsExpanded = false;
        window.clearTimeout(state.logSearchTimer);
        state.logSearchTimer = window.setTimeout(loadEvents, 250);
    });
    document.getElementById('eventLevel').addEventListener('change', () => {
        state.eventsExpanded = false;
        loadEvents();
    });
    document.getElementById('containerInterval').addEventListener('change', changeContainerInterval);
    document.getElementById('containerToggle').addEventListener('click', toggleContainers);
    document.getElementById('anomalyToggle').addEventListener('click', toggleAnomalies);
    document.getElementById('eventToggle').addEventListener('click', toggleEvents);
    document.getElementById('clearEventsButton').addEventListener('click', clearEvents);
    document.getElementById('signOutButton').addEventListener('click', signOut);
    document.getElementById('accountButton').addEventListener('click', () => {
        document.getElementById('passwordMessage').textContent = '';
        document.getElementById('accountDialog').showModal();
    });
    document.getElementById('passwordForm').addEventListener('submit', changePassword);
    document.getElementById('telegramTest')?.addEventListener('click', testTelegram);

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
    request.signal ||= AbortSignal.timeout(15000);
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
    const stale = snapshotIsStale(metric.ts, 90) || unavailable.has('stale');
    if (stale) ['cpu', 'memory', 'disk', 'swap', 'load', 'network', 'disk_io', 'cpu_temp'].forEach((name) => unavailable.add(name));
    updatePercentMetric('cpu', metric.cpu_percent, unavailable.has('cpu'));
    updatePercentMetric('memory', metric.ram_percent, unavailable.has('memory'));
    updatePercentMetric('disk', metric.disk_percent, unavailable.has('disk'));
    updatePercentMetric('swap', metric.swap_percent, unavailable.has('swap'));
    renderFilesystems(metric.filesystems, stale, unavailable.has('filesystems'));

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
        Number(metric.swap_percent) || 0,
        ...(metric.filesystems || []).filter((fs) => fs.stats_available).map((fs) => Number(fs.used_percent) || 0)
    );
    if (stale) setHealth('offline', 'Stale data');
    else if (peak > 90) setHealth('critical', 'Critical');
    else if (peak > 75) setHealth('warning', 'Watch');
    else if (unavailable.size > 0) setHealth('warning', 'Partial data');
    else setHealth('healthy', 'Healthy');
}

function renderFilesystems(filesystems, stale, partial) {
    const body = document.getElementById('filesystemTable');
    const cards = document.getElementById('filesystemCards');
    const system = document.getElementById('filesystemSystem');
    const disks = Array.isArray(filesystems) ? filesystems : [];
    const isSystemPartition = (fs) => ['/boot', '/boot/efi', '/efi'].includes(fs.mountpoint);
    const volumes = disks.filter((fs) => !isSystemPartition(fs)).sort((a, b) => {
        if (a.mountpoint === '/') return -1;
        if (b.mountpoint === '/') return 1;
        return String(a.mountpoint).localeCompare(String(b.mountpoint));
    });
    const partitions = disks.filter(isSystemPartition);
    const incomplete = partial || disks.some((fs) => !fs.stats_available);
    setText('filesystemState', stale ? 'stale data' : incomplete ? 'some disks unavailable' : disks.length ? `${volumes.length} storage volume${volumes.length === 1 ? '' : 's'} · 30s refresh` : 'unavailable');
    system.hidden = !partitions.length;
    setText('filesystemSystemCount', `(${partitions.length})`);
    cards.replaceChildren(...volumes.map((fs) => filesystemCard(fs, stale)));
    if (!disks.length) {
        cards.replaceChildren(emptyState('Mounted disk information unavailable.'));
    }
    const rows = partitions.map((fs) => {
        const available = fs.stats_available && !stale;
        const row = document.createElement('tr');
        row.dataset.state = !available ? 'unavailable' : fs.used_percent > 90 ? 'critical' : fs.used_percent > 75 ? 'warning' : 'healthy';
        const usage = cell(available ? formatPercent(fs.used_percent) : stale ? 'Stale' : 'Unavailable');
        usage.className = 'storage-usage';
        const mount = cell(fs.mountpoint);
        const device = document.createElement('small');
        device.textContent = fs.device;
        mount.appendChild(device);
        if (available) {
            const track = document.createElement('div');
            track.className = 'metric-track';
            const bar = document.createElement('i');
            bar.style.width = `${clamp(fs.used_percent)}%`;
            track.appendChild(bar);
            usage.appendChild(track);
        }
        row.append(
            mount, cell(fs.fstype),
            cell(available ? formatBytes(fs.total) : '—'),
            cell(available ? formatBytes(fs.used) : '—'),
            cell(available ? formatBytes(fs.available) : '—'), usage
        );
        return row;
    });
    body.replaceChildren(...rows);
}

function storageText(tag, className, value) {
    const element = document.createElement(tag);
    element.className = className;
    element.textContent = value;
    return element;
}

function filesystemCard(fs, stale) {
    const available = fs.stats_available && !stale;
    const status = !available ? 'unavailable' : fs.used_percent > 90 ? 'critical' : fs.used_percent > 75 ? 'warning' : 'healthy';
    const label = fs.mountpoint === '/' ? 'System disk' : fs.mountpoint === '/mnt/block' ? 'Block storage' : 'Additional storage';
    const card = document.createElement('article');
    card.className = 'storage-disk';
    card.dataset.state = status;
    card.dataset.kind = fs.mountpoint === '/' ? 'system' : 'additional';

    const header = document.createElement('div');
    header.className = 'storage-disk-heading';
    header.append(
        storageText('h4', 'storage-disk-name', label),
        storageText('span', 'storage-disk-status', !available ? stale ? 'Stale data' : 'Unavailable' : status === 'critical' ? 'Critical' : status === 'warning' ? 'Near capacity' : 'Normal')
    );
    const capacity = document.createElement('div');
    capacity.className = 'storage-capacity';
    capacity.append(
        storageText('strong', 'storage-used', available ? formatBytes(fs.used) : '—'),
        storageText('span', 'storage-total', available ? `used of ${formatBytes(fs.total)}` : 'Usage unavailable')
    );
    const track = document.createElement('div');
    track.className = 'storage-track';
    const bar = document.createElement('i');
    bar.style.width = `${available ? clamp(fs.used_percent) : 0}%`;
    track.appendChild(bar);
    const usage = document.createElement('div');
    usage.className = 'storage-disk-usage';
    usage.append(
        storageText('span', 'storage-percent', available ? `${formatPercent(fs.used_percent)} used` : '—'),
        storageText('strong', 'storage-free', available ? `${formatBytes(fs.available)} available` : 'Available space unknown')
    );
    card.append(header, storageText('p', 'storage-mount', fs.mountpoint), capacity, track, usage,
        storageText('div', 'storage-device', [fs.device, fs.fstype].filter(Boolean).join(' · ')));
    return card;
}

function snapshotIsStale(timestamp, maxAgeSeconds) {
    const age = Date.now() - new Date(timestamp || '').getTime();
    return !Number.isFinite(age) || age > maxAgeSeconds * 1000 || age < -5000;
}

function chartMetric(metric, field, availability) {
    return (metric.unavailable || []).includes(availability) ? null : Number(metric[field]) || 0;
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
        chartDataset('CPU', '#16d9e5', metrics.map((metric) => chartMetric(metric, 'cpu_percent', 'cpu')), 'percent', false),
        chartDataset('RAM', '#a84df1', metrics.map((metric) => chartMetric(metric, 'ram_percent', 'memory')), 'percent', false),
        chartDataset('Root disk', '#697070', metrics.map((metric) => chartMetric(metric, 'disk_percent', 'disk')), 'percent', true),
        chartDataset('Swap', '#f05b68', metrics.map((metric) => chartMetric(metric, 'swap_percent', 'swap')), 'percent', false),
        chartDataset('Net in', '#28d78c', metrics.map((metric) => chartMetric(metric, 'net_rx_bps', 'network')), 'bytes', true),
        chartDataset('Net out', '#d2a546', metrics.map((metric) => chartMetric(metric, 'net_tx_bps', 'network')), 'bytes', true)
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
    if (!state.chart || state.chartRange !== '1h' || snapshotIsStale(metric.ts, 90)) return;
    const label = formatChartTime(metric.ts);
    const labels = state.chart.data.labels;
    if (labels.at(-1) === label) return;
    labels.push(label);
    const values = [
        chartMetric(metric, 'cpu_percent', 'cpu'),
        chartMetric(metric, 'ram_percent', 'memory'),
        chartMetric(metric, 'disk_percent', 'disk'),
        chartMetric(metric, 'swap_percent', 'swap'),
        chartMetric(metric, 'net_rx_bps', 'network'),
        chartMetric(metric, 'net_tx_bps', 'network')
    ];
    state.chart.data.datasets.forEach((dataset, index) => dataset.data.push(values[index]));
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
        if (snapshotIsStale(details.checked_at, 150)) throw new Error('System details are stale');
        setText('kernelVersion', details.kernel_version || '—');
        const status = document.getElementById('systemState');
        status.textContent = details.reboot_required ? 'Restart required' : details.unavailable?.length ? 'Partial data' : 'Nominal';
        status.classList.toggle('warning', Boolean(details.reboot_required));
        renderProcessTable(details.processes || []);
        renderPortTable(details.listening_ports || [], details.unavailable?.includes('listening_ports'));
    } catch (error) {
        console.error('Unable to load system details:', error);
        setText('systemState', 'Unavailable');
        renderProcessTable([]);
        renderPortTable([], true);
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

function renderPortTable(ports, unavailable = false) {
    const body = document.getElementById('portTable');
    if (!ports.length) {
        body.replaceChildren(emptyTableRow(3, unavailable ? 'Host listening ports unavailable.' : 'No listening ports returned.'));
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

async function loadSystemServices() {
    if (state.systemServicesLoading) return;
    state.systemServicesLoading = true;
    try {
        const response = await apiFetch('/api/system/services');
        if (!response.ok) throw new Error(`System services returned ${response.status}`);
        renderSystemServices(await response.json());
    } catch (error) {
        console.error('Unable to load system services:', error);
        setText('serviceCollectionState', 'systemd status unavailable');
        renderServiceSummary(null);
        document.getElementById('serviceList').replaceChildren(serviceEmptyPanel('Unable to load system service status.'));
    } finally {
        state.systemServicesLoading = false;
    }
}

function renderSystemServices(snapshot) {
    const services = snapshot.services || [];
    if (!snapshot.enabled) {
        setText('serviceCollectionState', 'optional systemd monitoring · disabled');
        renderServiceSummary([]);
        document.getElementById('serviceList').replaceChildren(serviceEmptyPanel(
            snapshot.message || 'Set SYSTEMD_UNITS to choose services to monitor.'
        ));
        return;
    }
    if (!snapshot.available) {
        setText('serviceCollectionState', snapshot.message || 'systemd is unavailable');
        renderServiceSummary(null);
        document.getElementById('serviceList').replaceChildren(serviceEmptyPanel(
            'Sentinel cannot reach systemctl. Check the host bus mount and container setup.'
        ));
        return;
    }
    const checked = snapshot.checked_at ? new Date(snapshot.checked_at).toLocaleTimeString() : 'just now';
    setText('serviceCollectionState', `systemd status · checked ${checked}`);
    renderServiceSummary(services);
    const fragment = document.createDocumentFragment();
    [...services]
        .sort((left, right) => serviceStateRank(right.active_state) - serviceStateRank(left.active_state))
        .forEach((service) => fragment.appendChild(serviceCard(service)));
    document.getElementById('serviceList').replaceChildren(fragment);
}

function renderServiceSummary(services) {
    if (!services) {
        ['serviceCount', 'serviceRunningCount', 'serviceFailedCount', 'serviceNavCount'].forEach((id) => setText(id, '—'));
        setOverviewStatus('Service', '—', 'systemd unavailable', 'critical');
        return;
    }
    const running = services.filter((service) => service.active_state === 'active').length;
    const failed = services.filter((service) => service.active_state === 'failed').length;
    setText('serviceCount', services.length);
    setText('serviceRunningCount', running);
    setText('serviceFailedCount', failed);
    setText('serviceNavCount', failed);
    if (!services.length) {
        setOverviewStatus('Service', 'Off', 'monitoring disabled', 'disabled');
    } else {
        const stateName = failed > 0 ? 'critical' : running === services.length ? 'healthy' : 'warning';
        const detail = failed > 0 ? `${failed} failed` : `${running} of ${services.length} active`;
        setOverviewStatus('Service', `${running}/${services.length}`, detail, stateName);
    }
}

function serviceCard(service) {
    const unitName = service.unit || 'unknown.service';
    const stateName = service.active_state || 'unknown';
    const card = document.createElement('details');
    card.className = `panel service-card ${stateName}`;
    card.open = state.expandedServices.has(unitName);
    card.addEventListener('toggle', () => {
        if (card.open) state.expandedServices.add(unitName);
        else state.expandedServices.delete(unitName);
    });

    const summary = document.createElement('summary');
    summary.className = 'service-summary-row';
    const dot = document.createElement('span');
    dot.className = 'service-status-dot';
    dot.setAttribute('aria-hidden', 'true');
    const title = document.createElement('div');
    title.className = 'service-title';
    const unit = document.createElement('h3');
    unit.textContent = unitName;
    const description = document.createElement('span');
    description.textContent = service.description || service.load_state || 'No description';
    title.append(unit, description);
    const signals = document.createElement('span');
    signals.className = 'service-signals';
    signals.textContent = `${service.load_state || 'unknown'} · ${service.restarts ?? 0} restarts`;
    const badge = document.createElement('span');
    badge.className = `service-state ${stateName}`;
    badge.textContent = service.sub_state && service.sub_state !== stateName
        ? `${stateName} · ${service.sub_state}`
        : stateName;
    summary.append(dot, title, signals, badge);

    const meta = document.createElement('dl');
    meta.className = 'service-meta';
    meta.append(
        serviceMeta('Startup', service.unit_file_state || '—'),
        serviceMeta('Restart policy', service.restart_policy || '—'),
        serviceMeta('Restarts', service.restarts ?? 0),
        serviceMeta('Active since', service.active_since ? formatServiceTime(service.active_since) : (service.result || '—'))
    );

    const journal = document.createElement('details');
    journal.className = 'journal-list';
    journal.dataset.state = 'idle';
    const journalTitle = document.createElement('summary');
    journalTitle.className = 'journal-title';
    journalTitle.textContent = 'Recent journal · open to load';
    journal.appendChild(journalTitle);
    const journalContent = document.createElement('div');
    journalContent.appendChild(journalMessage('Open this section to load journal entries.'));
    journal.appendChild(journalContent);
    journal.addEventListener('toggle', () => {
        if (journal.open && journal.dataset.state === 'idle') {
            loadServiceJournal(unitName, journal, journalContent);
        }
    });

    card.append(summary, meta, journal);
    return card;
}

async function loadServiceJournal(unit, journal, content) {
	journal.dataset.state = 'loading';
	content.replaceChildren(journalMessage('Loading journal entries…'));
	try {
		const response = await apiFetch(`/api/system/services/${encodeURIComponent(unit)}/logs`);
		const payload = await response.json();
		if (!response.ok) throw new Error(payload.error || `Service journal returned ${response.status}`);
		if (!payload.available) {
			content.replaceChildren(journalMessage(payload.message || 'Journal is unavailable.'));
			journal.dataset.state = 'loaded';
			return;
		}
		const logs = payload.logs || [];
		if (!logs.length) {
			content.replaceChildren(journalMessage('No journal entries returned.'));
		} else {
			content.replaceChildren(...logs.map((entry) => journalRow(entry)));
		}
		journal.dataset.state = 'loaded';
	} catch (error) {
		console.error(`Unable to load journal for ${unit}:`, error);
		content.replaceChildren(journalMessage('Unable to load journal entries. Close and reopen to retry.'));
		journal.dataset.state = 'idle';
	}
}

function journalMessage(message) {
	const empty = document.createElement('div');
	empty.className = 'journal-empty';
	empty.textContent = message;
	return empty;
}

function serviceMeta(label, value) {
	const item = document.createElement('div');
	const term = document.createElement('dt');
	term.textContent = label;
	const detail = document.createElement('dd');
	detail.textContent = String(value);
	item.append(term, detail);
	return item;
}

function journalRow(entry) {
	const row = document.createElement('div');
	row.className = Number(entry.priority) <= 3 ? 'journal-row error' : 'journal-row';
	const time = document.createElement('time');
	time.textContent = formatEventTime(entry.timestamp);
	const message = document.createElement('span');
	message.textContent = entry.message || '';
	row.append(time, message);
	return row;
}

function serviceEmptyPanel(message) {
	const panel = document.createElement('article');
	panel.className = 'panel';
	panel.appendChild(emptyState(message));
	return panel;
}

function formatServiceTime(timestamp) {
	const date = new Date(timestamp);
	return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString();
}

async function loadContainers() {
    try {
        const response = await apiFetch('/api/containers');
        if (!response.ok) throw new Error(`Containers returned ${response.status}`);
        acceptContainerSnapshot(await response.json());
    } catch (error) {
        console.error('Unable to load container metrics:', error);
        state.containers = [];
        setText('containerStatus', 'Container telemetry unavailable');
        setText('containerCollectionState', 'collection failed');
        setOverviewStatus('Container', '—', 'telemetry unavailable', 'critical');
        renderContainerRows([]);
        scheduleContainerLoad(state.containerIntervalSeconds);
    }
}

function acceptContainerSnapshot(snapshot) {
    const seconds = [15, 30, 45, 60, 120].includes(Number(snapshot.interval_seconds))
        ? Number(snapshot.interval_seconds)
        : 30;
    state.containerIntervalSeconds = seconds;
    const control = document.getElementById('containerInterval');
    control.value = String(seconds);
    control.disabled = !snapshot.enabled;
    renderContainers(snapshot);
    scheduleContainerLoad(seconds);
}

function scheduleContainerLoad(seconds) {
    window.clearTimeout(state.containerRefreshTimer);
    state.containerRefreshTimer = window.setTimeout(loadContainers, Math.max(15, Number(seconds) || 30) * 1000);
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
    state.containers = containers;
    const interval = formatContainerInterval(snapshot.interval_seconds);
    if (!snapshot.enabled) {
        setText('containerCollectionState', 'optional Docker telemetry · disabled');
        setText('containerStatus', snapshot.message || 'Enable container metrics in configuration');
        renderContainerSummary(null);
        setOverviewStatus('Container', 'Off', 'telemetry disabled', 'disabled');
        renderContainerRows([], 'Container metrics are disabled. See the setup guide to enable them safely.');
        return;
    }
    const stale = snapshotIsStale(snapshot.collected_at, Math.max(90, 2 * Number(snapshot.interval_seconds || 30) + 30));
    if (!snapshot.available || stale) {
        state.containers = [];
        setText('containerCollectionState', `Docker telemetry · ${interval} collection · unavailable`);
        setText('containerStatus', stale ? 'Container data is stale' : snapshot.message || 'Docker metrics are unavailable');
        renderContainerSummary(null);
        setOverviewStatus('Container', '—', 'telemetry unavailable', 'critical');
        renderContainerRows([], 'Docker metrics are enabled but unavailable. Check the configured API or socket access.');
        return;
    }
    const unhealthy = containers.filter(isContainerUnhealthy).length;
    const running = containers.filter((item) => item.state === 'running').length;
    const incomplete = snapshot.partial || containers.some((item) => item.state === 'running' && !item.stats_available);
    const updated = snapshot.collected_at ? new Date(snapshot.collected_at).toLocaleTimeString() : 'just now';
    setText('containerCollectionState', `Docker telemetry · ${interval} collection · updated ${updated}`);
    setText('containerStatus', containers.length
        ? `${running} running / ${containers.length} total · ${unhealthy} not running or unhealthy${incomplete ? ' · partial stats' : ''}`
        : 'No containers found');
    setOverviewStatus(
        'Container',
        containers.length,
        unhealthy > 0 ? `${unhealthy} not running or unhealthy` : incomplete ? 'partial stats' : containers.length ? 'no unhealthy states reported' : 'no containers found',
        unhealthy > 0 ? 'critical' : incomplete || !containers.length ? 'warning' : 'healthy'
    );
    renderContainerSummary(containers);
    renderContainerRows(containers);
}

function formatContainerInterval(seconds) {
    return formatInterval(Number(seconds) || 30);
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
    const incomplete = containers.some((item) => item.state === 'running' && !item.stats_available);
    const cpuIncomplete = incomplete || containers.some((item) => item.state === 'running' && !item.cpu_available);
    setText('containerCPU', cpuIncomplete ? '—' : `${totals.cpu.toFixed(1)}%`);
    setText('containerMemory', incomplete ? '—' : formatBytes(totals.memory));
    setText('containerNetwork', incomplete ? '—' : `${formatBytes(totals.rx)} / ${formatBytes(totals.tx)}`);
}

function renderContainerRows(containers, emptyMessage = 'No running containers returned.') {
    const body = document.getElementById('containerTable');
    if (!containers.length) {
        updateListToggle('containerToggle', 0, listLimits.containers, false, 'Show all', 'Show top 10');
        body.replaceChildren(emptyTableRow(6, emptyMessage));
        return;
    }
    const ordered = [...containers].sort((left, right) => {
        const healthDifference = Number(isContainerUnhealthy(right)) - Number(isContainerUnhealthy(left));
        if (healthDifference !== 0) return healthDifference;
        return (Number(right.cpu_percent) || 0) - (Number(left.cpu_percent) || 0);
    });
    const visible = state.containersExpanded ? ordered : ordered.slice(0, listLimits.containers);
    updateListToggle(
        'containerToggle',
        ordered.length,
        listLimits.containers,
        state.containersExpanded,
        `Show all (${ordered.length})`,
        'Show top 10'
    );
    const fragment = document.createDocumentFragment();
    visible.forEach((container) => {
        const row = document.createElement('tr');
        const unhealthy = isContainerUnhealthy(container);
        row.className = unhealthy ? 'container-row-unhealthy' : '';
        const name = cell(container.name || container.id, container.image || '');
        name.className = 'container-name';
        const status = cell([container.status || container.state || '—', container.stats_message].filter(Boolean).join(' · '));
        status.className = unhealthy ? 'container-state unhealthy' : 'container-state';
        row.append(
            name,
            status,
            cell(container.stats_available && container.cpu_available ? formatPercent(container.cpu_percent) : '—'),
            cell(container.stats_available ? `${formatBytes(container.memory_used)} / ${formatBytes(container.memory_limit)} (${formatPercent(container.memory_percent)})` : '—'),
            cell(container.stats_available ? `${formatBytes(container.net_rx_bytes)} / ${formatBytes(container.net_tx_bytes)}` : '—'),
            cell(container.stats_available ? container.pids ?? '—' : '—')
        );
        fragment.appendChild(row);
    });
    body.replaceChildren(fragment);
}

function toggleContainers() {
    state.containersExpanded = !state.containersExpanded;
    renderContainerRows(state.containers);
}

function isContainerUnhealthy(container) {
    const stateName = String(container.state || '').toLowerCase();
    const status = String(container.status || '').toLowerCase();
    return (stateName && stateName !== 'running') ||
        ['unhealthy', 'restarting', 'exited', 'dead'].some((marker) => status.includes(marker));
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
    state.anomalies = anomalies;
    setText('anomalyCount', anomalies.length);
    const list = document.getElementById('anomalyList');
    if (!anomalies.length) {
        updateListToggle('anomalyToggle', 0, listLimits.anomalies, false, 'View all', 'Show important 10');
        list.replaceChildren(emptyState('No anomalies detected in the current baseline.'));
        return;
    }
    const important = [...anomalies].sort((left, right) => {
        const severityDifference = severityRank(right.severity) - severityRank(left.severity);
        if (severityDifference !== 0) return severityDifference;
        return new Date(right.ts).getTime() - new Date(left.ts).getTime();
    });
    const visible = state.anomaliesExpanded ? anomalies : important.slice(0, listLimits.anomalies);
    updateListToggle(
        'anomalyToggle',
        anomalies.length,
        listLimits.anomalies,
        state.anomaliesExpanded,
        `View all (${anomalies.length})`,
        'Show important 10'
    );
    const fragment = document.createDocumentFragment();
    visible.forEach((anomaly) => {
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

function toggleAnomalies() {
    state.anomaliesExpanded = !state.anomaliesExpanded;
    renderAnomalies(state.anomalies);
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
    const highestSeverity = events.reduce((result, event) => Math.max(result, severityRank(event.severity)), 0);
    setOverviewStatus(
        'Alert',
        events.length,
        events.length ? 'recent alert events' : 'no recent alerts',
        highestSeverity >= 3 ? 'critical' : events.length ? 'warning' : 'healthy'
    );
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
    state.events = events;
    setText('eventCount', events.length);
    const list = document.getElementById('eventList');
    if (!events.length) {
        updateListToggle('eventToggle', 0, listLimits.events, false, 'View all', 'Show recent 20');
        list.replaceChildren(emptyState('No events match the current filters.'));
        return;
    }
    const visible = state.eventsExpanded ? events : events.slice(0, listLimits.events);
    updateListToggle(
        'eventToggle',
        events.length,
        listLimits.events,
        state.eventsExpanded,
        `View all (${events.length})`,
        'Show recent 20'
    );
    const fragment = document.createDocumentFragment();
    visible.forEach((event) => {
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

function toggleEvents() {
    state.eventsExpanded = !state.eventsExpanded;
    renderEvents(state.events);
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

function setOverviewStatus(kind, value, detail, status) {
    const card = document.getElementById(`overview${kind}Card`);
    if (card) card.dataset.state = status;
    setText(`overview${kind}Value`, value);
    setText(`overview${kind}Detail`, detail);
}

function updateListToggle(id, total, limit, expanded, collapsedLabel, expandedLabel) {
    const button = document.getElementById(id);
    if (!button) return;
    button.hidden = total <= limit;
    button.disabled = total <= limit;
    button.setAttribute('aria-expanded', String(expanded));
    button.textContent = expanded ? expandedLabel : collapsedLabel;
}

function severityRank(severity) {
    switch (String(severity || '').toLowerCase()) {
    case 'critical':
    case 'error':
        return 3;
    case 'warning':
    case 'warn':
        return 2;
    case 'info':
        return 1;
    default:
        return 0;
    }
}

function serviceStateRank(activeState) {
    switch (String(activeState || '').toLowerCase()) {
    case 'failed':
        return 3;
    case 'inactive':
    case 'deactivating':
        return 2;
    case 'activating':
    case 'unknown':
        return 1;
    default:
        return 0;
    }
}

function formatInterval(seconds) {
    const value = Number(seconds) || 30;
    if (value >= 60) return `${value / 60}m`;
    return `${value}s`;
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

async function loadTelegram() {
    try {
        const response = await apiFetch('/api/notifications/telegram');
        if (!response.ok) throw new Error('Durum alınamadı');
        const status = await response.json();
        setText('telegramState', status.enabled ? 'Telegram etkin' : 'Telegram kapalı');
        const timestamp = (value) => value && !value.startsWith('0001-') ? new Date(value).toLocaleString('tr-TR') : 'Yok';
        setText('telegramDelivery', `Son başarılı: ${timestamp(status.last_success)} · Son başarısız: ${timestamp(status.last_failure)} · Bekleyen: ${status.pending}`);
        setText('telegramHealth', [status.storage_error, status.last_error, status.dropped ? `Atlanan bildirim/olay: ${status.dropped}` : ''].filter(Boolean).join(' · '));
        setText('telegramSSH', `SSH takibi: ${status.ssh}. Sentinel giriş takibi yalnızca panel girişlerini kapsar.`);
        const button = document.getElementById('telegramTest');
        if (button) button.hidden = !status.enabled || state.user?.role !== 'admin';
    } catch {
        setText('telegramState', 'Telegram durumu alınamıyor');
    }
}

async function testTelegram() {
    const button = document.getElementById('telegramTest');
    button.disabled = true;
    try {
        const response = await apiFetch('/api/notifications/telegram/test', { method: 'POST' });
        const payload = await response.json();
        setText('telegramTestResult', response.ok ? payload.message : (payload.error || 'Test isteği başarısız'));
        await loadTelegram();
    } catch {
        setText('telegramTestResult', 'Test isteği gönderilemedi.');
    } finally {
        button.disabled = false;
    }
}
