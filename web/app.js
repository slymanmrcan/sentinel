let metricsChart = null;
let chartRange = '1h';
let logSearchTimer = null;

document.addEventListener('DOMContentLoaded', () => {
    document.querySelectorAll('[data-range]').forEach((button) => {
        button.addEventListener('click', () => setChartRange(button.dataset.range));
    });

    document.getElementById('logSearch').addEventListener('input', () => {
        window.clearTimeout(logSearchTimer);
        logSearchTimer = window.setTimeout(fetchLogs, 250);
    });
    document.getElementById('logLevel').addEventListener('change', fetchLogs);
    document.getElementById('clearLogsButton').addEventListener('click', clearLogs);

    fetchRealtimeMetrics();
    fetchHistoryMetrics();
    fetchLogs();
    fetchSystemDetails();

    window.setInterval(fetchRealtimeMetrics, 2000);
    window.setInterval(fetchLogs, 5000);
    window.setInterval(fetchSystemDetails, 10000);
});

function setText(id, value) {
    const element = document.getElementById(id);
    if (element) {
        element.textContent = value;
    }
}

function clampPercent(value) {
    const numericValue = Number(value);
    if (!Number.isFinite(numericValue)) {
        return 0;
    }
    return Math.min(100, Math.max(0, numericValue));
}

function formatPercent(value) {
    return `${clampPercent(value).toFixed(1)}%`;
}

function formatBytesToGB(bytes) {
    const numericValue = Number(bytes);
    if (!Number.isFinite(numericValue) || numericValue <= 0) {
        return '0.00 GB';
    }
    return `${(numericValue / (1024 * 1024 * 1024)).toFixed(2)} GB`;
}

function formatUptime(uptimeSeconds) {
    const totalSeconds = Number(uptimeSeconds) || 0;
    const days = Math.floor(totalSeconds / 86400);
    const hours = Math.floor((totalSeconds % 86400) / 3600);
    const minutes = Math.floor((totalSeconds % 3600) / 60);
    return `${days}d ${hours}h ${minutes}m`;
}

function metricState(value) {
    const percent = clampPercent(value);
    if (percent > 85) {
        return { key: 'error', label: 'High' };
    }
    if (percent > 70) {
        return { key: 'warning', label: 'Watch' };
    }
    return { key: 'ok', label: 'Normal' };
}

function updateMetricCard(cardId, valueId, barId, stateId, value) {
    const percent = clampPercent(value);
    const state = metricState(percent);
    const card = document.getElementById(cardId);
    const bar = document.getElementById(barId);

    setText(valueId, formatPercent(percent));
    setText(stateId, state.label);
    card.dataset.state = state.key;
    bar.style.width = `${percent}%`;
}

function updateOverallStatus(cpu, ram, disk) {
    const highestValue = Math.max(clampPercent(cpu), clampPercent(ram), clampPercent(disk));
    const state = metricState(highestValue);
    const indicator = document.getElementById('statusIndicator');
    const statusText = document.getElementById('statusText');

    indicator.className = `pulse-indicator status-${state.key === 'ok' ? 'ok' : state.key}`;
    statusText.dataset.state = state.key;
    statusText.textContent = state.key === 'ok'
        ? 'System healthy'
        : state.key === 'warning'
            ? 'Needs attention'
            : 'High usage';
}

async function fetchRealtimeMetrics() {
    try {
        const response = await fetch('/api/metrics/realtime', {
            headers: { Accept: 'application/json' },
            cache: 'no-store'
        });
        if (!response.ok) {
            throw new Error(`Metrics API returned ${response.status}`);
        }

        const data = await response.json();

        setText('hostName', data.host_name || 'unknown');
        setText('osType', data.os || 'unknown');
        setText('uptime', formatUptime(data.uptime));
        setText('processes', data.processes ?? '--');
        setText('cpuModel', data.cpu_model || '--');

        updateMetricCard('cpuCard', 'cpuValue', 'cpuBar', 'cpuState', data.cpu_percent);
        updateMetricCard('ramCard', 'ramValue', 'ramBar', 'ramState', data.ram_percent);
        updateMetricCard('diskCard', 'diskValue', 'diskBar', 'diskState', data.disk_percent);

        setText('cpuCount', data.cpu_cores || '--');
        setText('cpuTemp', data.cpu_temp > 0 ? `${Number(data.cpu_temp).toFixed(1)}°C` : '--');
        setText('ramUsed', formatBytesToGB(data.ram_used));
        setText('ramTotal', formatBytesToGB(data.ram_total));
        setText('diskUsed', formatBytesToGB(data.disk_used));
        setText('diskTotal', formatBytesToGB(data.disk_total));

        const loadValues = [data.load_1, data.load_5, data.load_15]
            .map((value) => Number(value || 0).toFixed(2))
            .join(' · ');
        setText('cpuLoad', loadValues);
        setText(
            'swapInfo',
            `${formatBytesToGB(data.swap_used)} / ${formatBytesToGB(data.swap_total)} (${formatPercent(data.swap_percent)})`
        );

        updateOverallStatus(data.cpu_percent, data.ram_percent, data.disk_percent);
        setText('lastUpdated', `Updated ${new Date(data.ts || Date.now()).toLocaleTimeString()}`);

        appendRealtimePoint(data);
    } catch (error) {
        console.error('Error fetching realtime metrics:', error);
        const statusText = document.getElementById('statusText');
        statusText.dataset.state = 'error';
        statusText.textContent = 'Disconnected';
        document.getElementById('statusIndicator').className = 'pulse-indicator status-error';
        setText('lastUpdated', 'Metrics unavailable');
    }
}

function appendRealtimePoint(data) {
    if (!metricsChart || chartRange !== '1h' || !data.ts) {
        return;
    }

    const timeLabel = new Date(data.ts).toLocaleTimeString([], {
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit'
    });
    const labels = metricsChart.data.labels;
    if (labels.length > 0 && labels[labels.length - 1] === timeLabel) {
        return;
    }

    labels.push(timeLabel);
    metricsChart.data.datasets[0].data.push(clampPercent(data.cpu_percent));
    metricsChart.data.datasets[1].data.push(clampPercent(data.ram_percent));

    if (labels.length > 60) {
        labels.shift();
        metricsChart.data.datasets.forEach((dataset) => dataset.data.shift());
    }
    metricsChart.update('none');
}

async function fetchHistoryMetrics() {
    try {
        const response = await fetch(`/api/metrics/history?range=${chartRange}`, {
            headers: { Accept: 'application/json' },
            cache: 'no-store'
        });
        if (!response.ok) {
            throw new Error(`History API returned ${response.status}`);
        }

        const data = await response.json();
        const labels = data.map((item) => {
            const date = new Date(item.ts);
            if (chartRange === '1h') {
                return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
            }
            return date.toLocaleString([], { hour: '2-digit', minute: '2-digit' });
        });

        renderChart(
            labels,
            data.map((item) => clampPercent(item.cpu_percent)),
            data.map((item) => clampPercent(item.ram_percent))
        );
    } catch (error) {
        console.error('Error fetching history:', error);
    }
}

function renderChart(labels, cpuData, ramData) {
    const canvas = document.getElementById('metricsChart');
    if (!canvas || typeof Chart === 'undefined') {
        console.error('Chart.js is unavailable');
        return;
    }

    if (metricsChart) {
        metricsChart.data.labels = labels;
        metricsChart.data.datasets[0].data = cpuData;
        metricsChart.data.datasets[1].data = ramData;
        metricsChart.update();
        return;
    }

    metricsChart = new Chart(canvas.getContext('2d'), {
        type: 'line',
        data: {
            labels,
            datasets: [
                {
                    label: 'CPU',
                    data: cpuData,
                    borderColor: '#8b8cf8',
                    backgroundColor: 'rgba(139, 140, 248, 0.08)',
                    borderWidth: 2,
                    pointRadius: 0,
                    pointHoverRadius: 4,
                    tension: 0.32,
                    fill: true
                },
                {
                    label: 'Memory',
                    data: ramData,
                    borderColor: '#3dd6a1',
                    backgroundColor: 'rgba(61, 214, 161, 0.05)',
                    borderWidth: 2,
                    pointRadius: 0,
                    pointHoverRadius: 4,
                    tension: 0.32,
                    fill: true
                }
            ]
        },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            interaction: {
                intersect: false,
                mode: 'index'
            },
            animation: {
                duration: 300
            },
            plugins: {
                legend: {
                    align: 'end',
                    labels: {
                        color: '#aab4c2',
                        boxWidth: 8,
                        boxHeight: 8,
                        usePointStyle: true,
                        pointStyle: 'circle',
                        padding: 18,
                        font: {
                            family: 'Inter, ui-sans-serif, system-ui, sans-serif',
                            size: 11,
                            weight: 600
                        }
                    }
                },
                tooltip: {
                    backgroundColor: '#111927',
                    borderColor: '#2c394c',
                    borderWidth: 1,
                    titleColor: '#f4f7fb',
                    bodyColor: '#c2cad6',
                    padding: 10
                }
            },
            scales: {
                x: {
                    border: { display: false },
                    grid: { display: false },
                    ticks: {
                        color: '#788496',
                        maxTicksLimit: 8,
                        maxRotation: 0,
                        font: { size: 10 }
                    }
                },
                y: {
                    min: 0,
                    max: 100,
                    border: { display: false },
                    grid: { color: 'rgba(141, 153, 170, 0.10)' },
                    ticks: {
                        color: '#788496',
                        stepSize: 25,
                        callback: (value) => `${value}%`,
                        font: { size: 10 }
                    }
                }
            }
        }
    });
}

function setChartRange(range) {
    if (range !== '1h' && range !== '24h') {
        return;
    }
    chartRange = range;
    document.querySelectorAll('[data-range]').forEach((button) => {
        button.classList.toggle('active', button.dataset.range === range);
    });
    fetchHistoryMetrics();
}

function createLogRow(log) {
    const row = document.createElement('div');
    const level = ['INFO', 'WARN', 'ERROR'].includes(log.level) ? log.level : 'INFO';
    row.className = 'log-row';
    row.dataset.level = level;

    const timestamp = new Date(log.ts);
    const time = document.createElement('time');
    time.className = 'log-time';
    time.dateTime = timestamp.toISOString();
    time.title = timestamp.toLocaleString();
    time.textContent = timestamp.toLocaleTimeString([], {
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit'
    });

    const levelBadge = document.createElement('span');
    levelBadge.className = 'log-level';
    levelBadge.textContent = level;

    const source = document.createElement('span');
    source.className = 'log-source';
    source.title = String(log.source || 'external');
    source.textContent = String(log.source || 'external');

    const message = document.createElement('span');
    message.className = 'log-message';
    message.textContent = String(log.message || '');

    row.append(time, levelBadge, source, message);
    return row;
}

async function fetchLogs() {
    const search = document.getElementById('logSearch').value;
    const level = document.getElementById('logLevel').value;

    try {
        const response = await fetch(`/api/logs?level=${encodeURIComponent(level)}&query=${encodeURIComponent(search)}`, {
            headers: { Accept: 'application/json' },
            cache: 'no-store'
        });
        if (!response.ok) {
            throw new Error(`Logs API returned ${response.status}`);
        }

        const logs = await response.json();
        const logList = document.getElementById('logTerminal');
        setText('logCount', logs.length);

        if (logs.length === 0) {
            const emptyState = document.createElement('div');
            emptyState.className = 'empty-state';
            emptyState.textContent = 'No logs match the current filters.';
            logList.replaceChildren(emptyState);
            return;
        }

        const fragment = document.createDocumentFragment();
        logs.forEach((log) => fragment.appendChild(createLogRow(log)));
        logList.replaceChildren(fragment);
    } catch (error) {
        console.error('Error fetching logs:', error);
    }
}

async function clearLogs() {
    if (!window.confirm('Delete all logs from Sentinel? This cannot be undone.')) {
        return;
    }

    try {
        const response = await fetch('/api/logs', { method: 'DELETE' });
        if (!response.ok) {
            throw new Error(`Delete API returned ${response.status}`);
        }
        await fetchLogs();
    } catch (error) {
        console.error('Error clearing logs:', error);
    }
}

function createEmptyTableRow(columnCount, message) {
    const row = document.createElement('tr');
    const cell = document.createElement('td');
    cell.colSpan = columnCount;
    cell.className = 'empty-cell';
    cell.textContent = message;
    row.appendChild(cell);
    return row;
}

function createProcessRow(process) {
    const row = document.createElement('tr');

    const pid = document.createElement('td');
    pid.className = 'pid-cell';
    pid.textContent = String(process.pid ?? '--');

    const name = document.createElement('td');
    name.className = 'process-cell';
    name.textContent = String(process.name || 'Unknown');
    name.title = String(process.command || process.name || 'Unknown');

    const cpu = document.createElement('td');
    cpu.className = 'usage-cell align-right';
    const cpuValue = Number(process.cpu) || 0;
    cpu.textContent = `${cpuValue.toFixed(1)}%`;
    if (cpuValue > 50) {
        cpu.classList.add('is-danger');
    }

    const memory = document.createElement('td');
    memory.className = 'usage-cell align-right';
    const memoryValue = Number(process.memory) || 0;
    memory.textContent = `${memoryValue.toFixed(1)}%`;
    if (memoryValue > 10) {
        memory.classList.add('is-warning');
    }

    row.append(pid, name, cpu, memory);
    return row;
}

function createPortRow(port) {
    const row = document.createElement('tr');

    const portNumber = document.createElement('td');
    portNumber.className = 'port-cell';
    portNumber.textContent = `:${Number(port.port) || 0}`;

    const name = document.createElement('td');
    name.className = 'process-cell';
    name.textContent = String(port.name || 'Unknown');

    const pid = document.createElement('td');
    pid.className = 'pid-cell align-right';
    pid.textContent = Number(port.pid) > 0 ? String(port.pid) : '--';

    row.append(portNumber, name, pid);
    return row;
}

async function fetchSystemDetails() {
    try {
        const response = await fetch('/api/system/details', {
            headers: { Accept: 'application/json' },
            cache: 'no-store'
        });
        if (!response.ok) {
            throw new Error(`System details API returned ${response.status}`);
        }

        const data = await response.json();
        setText('kernelPill', data.kernel_version || '--');

        const rebootPill = document.getElementById('rebootPill');
        rebootPill.textContent = data.reboot_required ? 'Restart required' : 'No restart required';
        rebootPill.className = `inline-status ${data.reboot_required ? 'is-warning' : 'is-healthy'}`;

        const processesBody = document.getElementById('processesTableBody');
        if (!Array.isArray(data.processes) || data.processes.length === 0) {
            processesBody.replaceChildren(createEmptyTableRow(4, 'No processes returned.'));
        } else {
            const processFragment = document.createDocumentFragment();
            data.processes.forEach((process) => processFragment.appendChild(createProcessRow(process)));
            processesBody.replaceChildren(processFragment);
        }

        const portsBody = document.getElementById('portsTableBody');
        if (!Array.isArray(data.listening_ports) || data.listening_ports.length === 0) {
            portsBody.replaceChildren(createEmptyTableRow(3, 'No listening ports returned.'));
        } else {
            const portFragment = document.createDocumentFragment();
            data.listening_ports.forEach((port) => portFragment.appendChild(createPortRow(port)));
            portsBody.replaceChildren(portFragment);
        }
    } catch (error) {
        console.error('Error fetching system details:', error);
    }
}
