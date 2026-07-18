let metricsChart = null;
let chartRange = '1h';

document.addEventListener('DOMContentLoaded', () => {
    // Initial fetch
    fetchSystemInfo();
    fetchRealtimeMetrics();
    fetchHistoryMetrics();
    fetchLogs();

    // Setup periodic polling
    setInterval(fetchRealtimeMetrics, 2000);
    setInterval(fetchLogs, 3000);
});

// Helper: Format Bytes to GB
function formatBytesToGB(bytes) {
    if (!bytes) return '0.00';
    return (bytes / (1024 * 1024 * 1024)).toFixed(2);
}

// 1. Fetch system details (non-dynamic info)
async function fetchSystemInfo() {
    try {
        const res = await fetch('/api/metrics/realtime');
        if (!res.ok) throw new Error('API error');
        const data = await res.json();
        
        document.getElementById('hostName').textContent = data.host_name || 'unknown';
        document.getElementById('osType').textContent = data.os || 'unknown';
    } catch (err) {
        console.error('Error fetching system info:', err);
    }
}

// 2. Fetch realtime metrics and update Cards
async function fetchRealtimeMetrics() {
    try {
        const res = await fetch('/api/metrics/realtime');
        if (!res.ok) throw new Error('API error');
        const data = await res.json();

        // Update Uptime
        const uptimeSeconds = data.uptime || 0;
        const days = Math.floor(uptimeSeconds / 86400);
        const hours = Math.floor((uptimeSeconds % 86400) / 3600);
        const minutes = Math.floor((uptimeSeconds % 3600) / 60);
        document.getElementById('uptime').textContent = `${days}d ${hours}h ${minutes}m`;
        document.getElementById('processes').textContent = data.processes || '--';

        // Update CPU Card
        const cpu = data.cpu_percent ? data.cpu_percent.toFixed(1) : '0.0';
        document.getElementById('cpuBadge').textContent = `${cpu}%`;
        document.getElementById('cpuBadgeInner').textContent = `${cpu}%`;
        document.getElementById('cpuCircle').setAttribute('stroke-dasharray', `${Math.round(data.cpu_percent || 0)}, 100`);
        document.getElementById('cpuCount').innerHTML = `<strong>${data.cpu_cores || '--'}</strong>`;
        const temp = data.cpu_temp && data.cpu_temp > 0 ? `${data.cpu_temp.toFixed(1)}°C` : '--';
        document.getElementById('cpuTemp').innerHTML = `<strong>${temp}</strong>`;
        const load1 = data.load_1 ? data.load_1.toFixed(2) : '0.00';
        const load5 = data.load_5 ? data.load_5.toFixed(2) : '0.00';
        const load15 = data.load_15 ? data.load_15.toFixed(2) : '0.00';
        document.getElementById('cpuLoad').innerHTML = `<strong>${load1}, ${load5}, ${load15}</strong>`;
        document.getElementById('cpuModel').innerHTML = `<strong>${data.cpu_model || '--'}</strong>`;
        setCircleColor('cpuCircle', data.cpu_percent);

        // Update RAM Card
        const ram = data.ram_percent ? data.ram_percent.toFixed(1) : '0.0';
        document.getElementById('ramBadge').textContent = `${ram}%`;
        document.getElementById('ramBadgeInner').textContent = `${ram}%`;
        document.getElementById('ramCircle').setAttribute('stroke-dasharray', `${Math.round(data.ram_percent || 0)}, 100`);
        document.getElementById('ramUsed').innerHTML = `<strong>${formatBytesToGB(data.ram_used)} GB</strong>`;
        document.getElementById('ramTotal').innerHTML = `<strong>${formatBytesToGB(data.ram_total)} GB</strong>`;
        const swapPercent = data.swap_percent ? data.swap_percent.toFixed(1) : '0.0';
        document.getElementById('swapInfo').innerHTML = `<strong>${formatBytesToGB(data.swap_used)} GB / ${formatBytesToGB(data.swap_total)} GB (${swapPercent}%)</strong>`;
        setCircleColor('ramCircle', data.ram_percent);

        // Update Disk Card
        const disk = data.disk_percent ? data.disk_percent.toFixed(1) : '0.0';
        document.getElementById('diskBadge').textContent = `${disk}%`;
        document.getElementById('diskBadgeInner').textContent = `${disk}%`;
        document.getElementById('diskCircle').setAttribute('stroke-dasharray', `${Math.round(data.disk_percent || 0)}, 100`);
        document.getElementById('diskUsed').innerHTML = `<strong>${formatBytesToGB(data.disk_used)} GB</strong>`;
        document.getElementById('diskTotal').innerHTML = `<strong>${formatBytesToGB(data.disk_total)} GB</strong>`;
        setCircleColor('diskCircle', data.disk_percent);

        // Update Main Status Indicator
        const maxVal = Math.max(data.cpu_percent || 0, data.ram_percent || 0);
        const indicator = document.getElementById('statusIndicator');
        indicator.className = 'pulse-indicator';
        if (maxVal > 85) {
            indicator.classList.add('status-error');
        } else if (maxVal > 70) {
            indicator.classList.add('status-warning');
        } else {
            indicator.classList.add('status-ok');
        }

        // Add point to chart if chart is loaded and running realtime
        if (metricsChart && data.ts) {
            const timeStr = new Date(data.ts).toLocaleTimeString();
            
            // Only push if the last label is different to avoid duplicate points on poll
            const labels = metricsChart.data.labels;
            if (labels.length === 0 || labels[labels.length - 1] !== timeStr) {
                metricsChart.data.labels.push(timeStr);
                metricsChart.data.datasets[0].data.push(data.cpu_percent);
                metricsChart.data.datasets[1].data.push(data.ram_percent);

                // Limit realtime labels to 30 items
                if (labels.length > 30) {
                    metricsChart.data.labels.shift();
                    metricsChart.data.datasets[0].data.shift();
                    metricsChart.data.datasets[1].data.shift();
                }
                metricsChart.update('none'); // silent update
            }
        }
    } catch (err) {
        console.error('Error fetching realtime metrics:', err);
    }
}

// Adjust colors for circular charts based on usage levels
function setCircleColor(circleId, value) {
    const el = document.getElementById(circleId);
    if (!el) return;
    if (value > 85) {
        el.style.stroke = '#f87171'; // Danger (red)
    } else if (value > 70) {
        el.style.stroke = '#fbbf24'; // Warning (yellow)
    } else {
        // Default base colors
        if (circleId === 'cpuCircle') el.style.stroke = '#818cf8';
        if (circleId === 'ramCircle') el.style.stroke = '#10b981';
        if (circleId === 'diskCircle') el.style.stroke = '#06b6d4';
    }
}

// 3. Fetch historical metrics
async function fetchHistoryMetrics() {
    try {
        const res = await fetch(`/api/metrics/history?range=${chartRange}`);
        if (!res.ok) throw new Error('API error');
        const data = await res.json();
        
        const labels = [];
        const cpuData = [];
        const ramData = [];

        data.forEach(item => {
            const date = new Date(item.ts);
            labels.push(chartRange === '1h' ? date.toLocaleTimeString() : date.toLocaleString());
            cpuData.push(item.cpu_percent);
            ramData.push(item.ram_percent);
        });

        renderChart(labels, cpuData, ramData);
    } catch (err) {
        console.error('Error fetching history:', err);
    }
}

// Render or Update Chart.js
function renderChart(labels, cpuData, ramData) {
    const ctx = document.getElementById('metricsChart').getContext('2d');
    
    if (metricsChart) {
        metricsChart.data.labels = labels;
        metricsChart.data.datasets[0].data = cpuData;
        metricsChart.data.datasets[1].data = ramData;
        metricsChart.update();
        return;
    }

    metricsChart = new Chart(ctx, {
        type: 'line',
        data: {
            labels: labels,
            datasets: [
                {
                    label: 'CPU Usage (%)',
                    data: cpuData,
                    borderColor: '#6366f1',
                    backgroundColor: 'rgba(99, 102, 241, 0.05)',
                    tension: 0.3,
                    fill: true,
                    borderWidth: 2,
                    pointRadius: 2
                },
                {
                    label: 'RAM Usage (%)',
                    data: ramData,
                    borderColor: '#10b981',
                    backgroundColor: 'rgba(16, 185, 129, 0.05)',
                    tension: 0.3,
                    fill: true,
                    borderWidth: 2,
                    pointRadius: 2
                }
            ]
        },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            plugins: {
                legend: {
                    labels: {
                        color: '#9ca3af',
                        font: { family: 'Outfit' }
                    }
                }
            },
            scales: {
                x: {
                    grid: { color: 'rgba(255, 255, 255, 0.03)' },
                    ticks: { color: '#9ca3af', font: { family: 'Outfit' } }
                },
                y: {
                    min: 0,
                    max: 100,
                    grid: { color: 'rgba(255, 255, 255, 0.03)' },
                    ticks: { color: '#9ca3af', font: { family: 'Outfit' } }
                }
            }
        }
    });
}

// Chart Range Selection
function setChartRange(range) {
    chartRange = range;
    document.getElementById('btn-1h').classList.toggle('active', range === '1h');
    document.getElementById('btn-24h').classList.toggle('active', range === '24h');
    fetchHistoryMetrics();
}

// 4. Fetch System Logs
async function fetchLogs() {
    const search = document.getElementById('logSearch').value;
    const level = document.getElementById('logLevel').value;

    try {
        const res = await fetch(`/api/logs?level=${level}&query=${encodeURIComponent(search)}`);
        if (!res.ok) throw new Error('API error');
        const logs = await res.json();

        const terminal = document.getElementById('logTerminal');
        
        if (logs.length === 0) {
            terminal.innerHTML = '<div class="log-line system-line">[SYSTEM] No logs matching the filters found.</div>';
            return;
        }

        let html = '';
        logs.forEach(log => {
            const timeStr = new Date(log.ts).toLocaleString();
            let levelClass = 'log-level-info';
            if (log.level === 'WARN') levelClass = 'log-level-warn';
            if (log.level === 'ERROR') levelClass = 'log-level-error';

            html += `
                <div class="log-line">
                    <span class="log-time">[${timeStr}]</span>
                    <span class="${levelClass}">${log.level}</span>
                    <span class="log-source">[${log.source}]</span>
                    <span class="log-msg">${escapeHTML(log.message)}</span>
                </div>
            `;
        });

        // Store scroll position, check if scrolled to bottom
        const isScrolledToBottom = terminal.scrollHeight - terminal.clientHeight <= terminal.scrollTop + 30;

        terminal.innerHTML = html;

        // Auto scroll if user was at the bottom
        if (isScrolledToBottom) {
            terminal.scrollTop = terminal.scrollHeight;
        }
    } catch (err) {
        console.error('Error fetching logs:', err);
    }
}

// Clear all logs in DuckDB
async function clearLogs() {
    if (!confirm('Are you sure you want to delete all logs from DuckDB?')) return;
    try {
        const res = await fetch('/api/logs', { method: 'DELETE' });
        if (!res.ok) throw new Error('Delete error');
        fetchLogs();
    } catch (err) {
        console.error('Error clearing logs:', err);
    }
}

// Utility: Escape HTML
function escapeHTML(str) {
    if (!str) return '';
    return str.replace(/[&<>'"]/g, 
        tag => ({
            '&': '&amp;',
            '<': '&lt;',
            '>': '&gt;',
            "'": '&#39;',
            '"': '&quot;'
        }[tag] || tag)
    );
}
