# Sentinel

Kompakt, tek binary olarak dağıtılan host telemetry ve anomaly detection paneli.

- Go + DuckDB
- `cmd/api` ve `internal/*` paket yapısı
- HttpOnly oturum cookie’si, CSRF koruması ve brute-force kilidi
- CPU, bellek, swap, disk, load, network ve disk I/O telemetrisi
- Opsiyonel container bazlı CPU, working-set RAM, network ve PID telemetrisi
- Rolling baseline, z-score anomaly detection ve threshold alert’leri
- Gömülü, responsive web arayüzü

<details open>
<summary><strong>Türkçe</strong></summary>

## Mimari

```text
sentinel/
├── cmd/api/                 # Uygulama entrypoint'i
├── internal/
│   ├── app/                 # Başlatma ve graceful shutdown
│   ├── auth/                # Oturum, parola, CSRF ve login lockout
│   ├── config/              # Ortam değişkeni kontratı
│   ├── httpapi/             # Router, middleware ve HTTP handler'ları
│   ├── monitor/             # Host collector, anomaly ve alert engine
│   └── store/               # DuckDB migration ve sorguları
├── web/                     # Binary içine gömülen UI dosyaları
├── Dockerfile
└── docker-compose.yml
```

İstek akışı:

```mermaid
flowchart LR
    Browser["Browser UI"] --> API["cmd/api + internal/httpapi"]
    API --> Auth["internal/auth"]
    API --> Monitor["internal/monitor"]
    Auth --> DB[("DuckDB")]
    Monitor --> DB
    Monitor --> Host["/proc · /sys · host root"]
    Monitor -. opt-in .-> Docker["Docker Engine API"]
```

## İlk çalıştırma

Gereksinimler:

- Go `1.25.12`
- CGO destekli toolchain
- İlk admin için en az 8 karakterlik parola

```bash
cp .env.example .env
# .env içindeki ADMIN_PASSWORD değerini değiştir
make run
```

Panel: `http://localhost:8000`

İlk açılışta `ADMIN_LOGIN`, `ADMIN_NAME` ve `ADMIN_PASSWORD` ile admin
oluşturulur. Sonraki başlangıçlarda bu değerler mevcut admin hesapla
senkronlanır; böylece `.env` üzerinden kullanıcı adı/parola kurtarma ve reset
yapılabilir. `ADMIN_PASSWORD` ortamda kalırsa panelden yapılan parola değişikliği
sonraki restartta tekrar `.env` değerine döner.

> Eski kurulumların geçişi için `ADMIN_EMAIL`, `AUTH_USER` ve `AUTH_PASSWORD`,
> yeni admin değişkenleri verilmediğinde fallback olarak okunur.

## Auth modeli

- Tarayıcı yalnızca rastgele, opaque bir `sentinel_session` cookie’si alır.
- Cookie `HttpOnly` ve `SameSite=Strict` olarak ayarlanır.
- Sunucuda session token’ın kendisi değil SHA-256 özeti tutulur.
- Yazma istekleri session’a bağlı `X-CSRF-Token` ister.
- Beş hatalı girişten sonra login + IP çifti 15 dakika kilitlenir.
- Parola bcrypt ile hashlenir.
- Parola uzunluğu 8 karakter ile bcrypt sınırı olan 72 byte arasındadır.
- Parola değişimi kullanıcının tüm aktif oturumlarını kapatır.
- `/healthz` public; dashboard ve `/api/*` auth korumalıdır.

`AUTH_COOKIE_SECURE=true` yalnızca servis gerçekten HTTPS üzerinden
yayınlanıyorsa kullanılmalıdır. Sentinel’i ağ üzerinden HTTP ile açmayın; HTTPS
reverse proxy arkasında tutun.

## Ortam değişkenleri

| Değişken | Varsayılan | Açıklama |
|---|---:|---|
| `PORT` | `8000` | HTTP portu |
| `DB_PATH` | `metrics.db` | DuckDB dosyası |
| `ADMIN_LOGIN` | `admin` | Admin kullanıcı adı |
| `ADMIN_NAME` | `Sentinel Admin` | Admin görünen adı |
| `ADMIN_PASSWORD` | yok | Admin parolası/reset değeri, min. 8 karakter |
| `AUTH_COOKIE_SECURE` | `false` | HTTPS ortamında `true` |
| `AUTH_SESSION_TTL` | `12h` | `15m`–`720h` arası oturum süresi |
| `AUTH_ALLOWED_ORIGINS` | boş | Virgülle ayrılmış public origin listesi |
| `TRUST_PROXY_HEADERS` | `false` | Yalnız güvenilir proxy arkasında `true` |
| `HOST_ROOT` | boş | Host root mount yolu |
| `HOST_SYS` | boş | Host sysfs mount yolu |
| `NETWORK_INTERFACES` | boş | Sayaçlara dahil edilecek virgülle ayrılmış arayüzler |
| `CONTAINER_METRICS_ENABLED` | `false` | Docker container telemetrisini açar |
| `CONTAINER_COLLECTION_INTERVAL` | `15s` | İlk container ölçüm aralığı: `15s`, `30s`, `45s`, `1m`, `2m` |
| `CONTAINER_API_URL` | boş | Korunan Docker API/proxy adresi |
| `DOCKER_SOCKET` | `/var/run/docker.sock` | API URL yoksa kullanılan Unix socket |

Uygulama başlangıçta çalışma dizinindeki `.env` dosyasını yükler; gerçek ortam
değişkenleri `.env` değerlerinden önceliklidir. `.env` Git tarafından yok
sayılır.

HTTPS reverse proxy `Host` header’ını koruyorsa ayrıca bir ayar gerekmez. Proxy
upstream’e `Host: sentinel:8000` gibi farklı bir değer gönderiyorsa public adresi
açıkça tanımlayın:

```env
AUTH_COOKIE_SECURE=true
AUTH_ALLOWED_ORIGINS=https://sentinel.example.com
```

`TRUST_PROXY_HEADERS=true` alternatifi yalnız Sentinel’e doğrudan internetten
erişilemiyor ve tüm istekler güvenilir proxy’den geliyorsa kullanılmalıdır.
`AUTH_COOKIE_SECURE` yalnız cookie güvenliğini yönetir; origin doğrulamasının
şemasını değiştirmez.

## API

| Method | Route | Auth | Açıklama |
|---|---|---|---|
| `GET` | `/healthz` | Hayır | DB liveness |
| `POST` | `/api/auth/login` | Hayır | Oturum aç |
| `GET` | `/api/auth/me` | Evet | Kullanıcı + CSRF token |
| `POST` | `/api/auth/logout` | Evet + CSRF | Oturumu kapat |
| `POST` | `/api/auth/password` | Evet + CSRF | Parola değiştir |
| `GET` | `/api/metrics/realtime` | Evet | Son host snapshot |
| `GET` | `/api/metrics/history?range=1h` | Evet | `1h`, `6h`, `24h`, `7d` |
| `GET` | `/api/metrics/summary` | Evet | Rolling baseline |
| `GET` | `/api/anomalies` | Evet | Anomaly geçmişi |
| `GET` | `/api/alerts/rules` | Evet | Aktif threshold kuralları |
| `GET` | `/api/alerts/events` | Evet | Alert geçmişi |
| `GET/POST/DELETE` | `/api/logs` | Evet | Event akışı |
| `GET` | `/api/system/details` | Evet | Process, port, kernel |
| `GET` | `/api/containers` | Evet | Opsiyonel container snapshot'ı |
| `PUT` | `/api/containers/settings` | Evet + CSRF | Container ölçüm aralığını değiştirir |
| `GET` | `/api/export/*.csv` | Evet | CSV export |

Network kartı anlık inbound/outbound hızını ve host açılışından beri biriken
inbound, outbound ve toplam byte sayaçlarını gösterir. Bu host seviyesinde tüm
ağ arayüzlerinin toplamıdır; bridge, veth ve loopback trafiğini içerebileceği
için internet sağlayıcısı fatura ölçümü olarak değerlendirilmemelidir.
`NETWORK_INTERFACES=eth0,wlan0` gibi açık bir liste verilerek hangi sayaçların
dahil olacağı sınırlandırılabilir.

## Container telemetrisi

Container görünümü aynı dashboard içinde ayrı bir bölümdür ve varsayılan olarak
kapalıdır. En güvenli tercih, sadece gerekli read-only route'ları açan,
kimlik doğrulamalı ve ağ ile sınırlandırılmış bir Docker API proxy'sidir:

```env
CONTAINER_METRICS_ENABLED=true
CONTAINER_API_URL=http://docker-metrics-proxy:2375
```

Doğrudan `/var/run/docker.sock` bağlantısı da desteklenir ancak Docker daemon
erişimi pratikte host üzerinde çok yüksek yetki verir. Bu yüzden varsayılan
Compose dosyası socket mount etmez. Kurulum seçenekleri ve metrik formülleri
için [container metrics rehberine](docs/container-metrics.md) bakın.

Güvenilir tek-host kurulumu için repository'deki açık opt-in override'ı
kullanın:

```bash
docker compose -f docker-compose.yml -f docker-compose.containers.yml up -d --build
```

Bu komut container telemetrisini açar ve Linux Docker socket'ini bağlar.
Varsayılan `docker compose up` socket erişimi vermez.

Header'daki interval seçici `15s`, `30s`, `45s`, `1m` ve `2m` seçeneklerini
gerçek collector timer'ına uygular. Seçim DuckDB'ye kaydedilir ve restart sonrası
korunur; `.env` değeri yalnız henüz kayıtlı bir UI seçimi yokken başlangıç
varsayılanıdır.

Cookie ve CSRF ile örnek:

```bash
curl -c /tmp/sentinel.cookies \
  -H 'Content-Type: application/json' \
  -d '{"login":"admin","password":"YOUR_LONG_PASSWORD"}' \
  http://localhost:8000/api/auth/login
```

Login response içindeki `csrf_token`, `POST`/`DELETE` isteklerinde
`X-CSRF-Token` header’ı olarak gönderilir. Parolayı shell history’ye yazmamak
için gerçek kullanımda güvenli bir secret yöntemi tercih edin.

## Docker Compose

```bash
cp .env.example .env
# Güçlü admin parolası ayarla
docker compose build
docker compose up -d
```

Compose kontratı:

- Host port yayınlamaz; yalnız external `infra_net` üzerinde `8000` expose eder.
- `pid: host`, read-only `/proc`, `/sys` ve host root mountları kullanır.
- Root filesystem read-only, capability’ler drop, `no-new-privileges` açıktır.
- `/data` kalıcı ve yazılabilirdir.

Önce ağı oluşturun:

```bash
docker network create infra_net
```

Reverse proxy aynı network’ten `sentinel:8000` hedefine bağlanmalıdır.

## Kalite kontrolleri

```bash
make test
make vet
make build
docker compose config --quiet
```

CI her push/PR için format, race detector, coverage, vet, Go build ve Docker
image build kontrollerini çalıştırır. Gerçek Linux host ve Docker karşılaştırma
adımları [Linux validation checklist](docs/linux-validation.md) içinde yer alır.

`make vulncheck` güncel vulnerability verisi için ağ erişimi ister.

## Veri ve retention

- Metrics: 30 gün
- Anomalies: 30 gün
- Alert events: 30 gün
- System events/logs: 7 gün
- Süresi biten sessions: saatlik temizlenir

`data/`, `metrics.db`, DuckDB WAL dosyaları, `.env` ve binary çıktıları Git’e
alınmaz.

</details>

<details>
<summary><strong>English</strong></summary>

## Overview

Sentinel is a compact, single-binary host telemetry console backed by DuckDB.
It collects CPU, memory, swap, disk, load, network, and disk I/O metrics; builds a
rolling statistical baseline; detects z-score anomalies; and evaluates
threshold alert rules. Optional Docker Engine telemetry adds per-container CPU,
memory working set, cumulative network counters, and PID counts.

The Go code is split into:

- `cmd/api`: executable entrypoint
- `internal/app`: lifecycle and graceful shutdown
- `internal/auth`: sessions, password hashing, CSRF, and login lockout
- `internal/config`: environment contract
- `internal/httpapi`: router, middleware, and handlers
- `internal/monitor`: collection, anomaly detection, and alert evaluation
- `internal/store`: DuckDB schema and queries
- `web`: embedded responsive UI

## First run

```bash
cp .env.example .env
# Replace ADMIN_PASSWORD with a value of at least 8 characters.
make run
```

Open `http://localhost:8000`.

Sentinel creates and synchronizes the administrator from `ADMIN_LOGIN`,
`ADMIN_NAME`, and `ADMIN_PASSWORD`. This makes the environment values the
recovery/reset mechanism for an existing database. If `ADMIN_PASSWORD` remains
configured, a password changed from the account menu will return to the
environment value after restart.

Legacy `ADMIN_EMAIL`, `AUTH_USER`, and `AUTH_PASSWORD` values remain accepted
as fallbacks when the new variables are absent.

## Authentication and security

- Opaque server-side sessions; only the token hash is persisted
- HttpOnly, SameSite=Strict session cookie
- Session-bound CSRF token for state-changing requests
- bcrypt password hashing
- 15-minute lockout after five failed attempts per login and client IP
- Password changes revoke all sessions
- Public `/healthz`; authenticated dashboard and API

Set `AUTH_COOKIE_SECURE=true` only when the browser reaches Sentinel over
HTTPS. Keep network deployments behind an HTTPS reverse proxy.

When the proxy rewrites the upstream Host header, set the public browser origin
explicitly:

```env
AUTH_COOKIE_SECURE=true
AUTH_ALLOWED_ORIGINS=https://sentinel.example.com
```

## Docker

```bash
docker network create infra_net
cp .env.example .env
docker compose build
docker compose up -d
```

Compose exposes port `8000` only on the external `infra_net`; it does not
publish a host port. The container uses a read-only root filesystem, dropped
capabilities, no-new-privileges, and read-only host inspection mounts.

Container telemetry is disabled by default. Prefer a protected, read-only
Docker API proxy. Directly mounting `docker.sock` grants highly privileged
daemon access even if the mount itself is marked read-only, so the default
Compose file deliberately does not mount it. See
[container metrics](docs/container-metrics.md).

For an explicitly trusted single-host installation, use the opt-in override:

```bash
docker compose -f docker-compose.yml -f docker-compose.containers.yml up -d --build
```

The header interval selector changes the actual Docker collection timer and
persists the selected `15s`, `30s`, `45s`, `1m`, or `2m` value in DuckDB.

## Validation

```bash
make test
make vet
make build
docker compose config --quiet
```

CI also runs the race detector and builds the Docker image. Use the
[Linux validation checklist](docs/linux-validation.md) for real host and cgroup
verification.

Metrics, anomalies, and alerts are retained for 30 days; system events for 7
days. Database files, `.env`, WAL files, and binaries are ignored by Git.

The network card shows live inbound/outbound throughput and the inbound,
outbound, and combined byte counters accumulated since the host booted. These
are host-wide interface counters and can include bridge, veth, and loopback
traffic, so they should not be treated as ISP billing measurements.

</details>

## License

Sentinel is available under the [MIT License](LICENSE).
