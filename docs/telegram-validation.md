# Telegram bildirimleri doğrulama notları

Tarih: 21 Eylül 2026. Çalışan Sentinel servisi yeniden başlatılmadı; dağıtım
ve gerçek Telegram gönderimi yapılmadı. Derleme çıktıları `/tmp` altına yazıldı.
Testler kendi geçici DuckDB dosyalarını kullandı; mevcut veritabanına dokunulmadı.

## İşlev ve güvenlik kontrolleri

- `go test -race -coverprofile=/tmp/sentinel-telegram-coverage.out ./...` başarılı.
  Son eklenen senaryolar ayrıca `go test -race ./internal/notify` ile doğrulandı.
- `node --test tests/dashboard.test.cjs`: 11 test başarılı.
- `go vet ./...`, `go build -o /tmp/sentinel-telegram-validation ./cmd/api`,
  `git diff --check` ve Go biçim kontrolleri başarılı.
- `golangci-lint run ./internal/notify`: sıfır bulgu.
- `make check`: Go/panel testleri ve vet geçti; mevcut kodun lint bulgularında durdu.
  Tam lint çalıştırması, bu değişiklik dışında kalan satırlarda 11 bulgu raporladı:
  kontrol edilmeyen `Close` sonuçları, Docker hata metinlerinin büyük harfle
  başlaması ve mevcut `cpuTimes.Total()` kullanımı. Bunlar bildirim özelliği
  kapsamında değiştirilmedi.
- Kalan kontrol `make vulncheck` ayrıca çalıştırıldı. Yerel `go1.26.5 darwin/arm64`
  standart kitaplığında beş erişilebilir bulgu raporlandı: GO-2026-6218,
  GO-2026-6090, GO-2026-6089, GO-2026-5972 ve GO-2026-5026. Taramanın bildirdiği
  düzeltme sürümü Go 1.26.6. Ayrıca çağrı izi bulunmayan bağımlılık bulguları var.
  Dolayısıyla tüm `make check` akışı yeşil değildir; araç zinciri/güvenlik
  güncellemesi dağıtımdan önce ayrıca ele alınmalıdır. Yerel Go kurulumu,
  `go.mod` ve Docker taban sürümü bu çalışmada değiştirilmedi.

Senaryolar: dört metrikte alarm/toparlanma, kısa dalgalanma, histerezis, eski/eksik
ölçüm, kaybolan disk, systemd failed/active ve erişilemeyen durum, yinelenen
ölçüm, özet saatleri/saat dilimi/yaz saati geçişleri, aynı DuckDB'nin kapatılıp
açılması, outbox önceliği/sınırı/ömrü, eski bekleyen alarmın son durumla
birleştirilmesi, teslimat sonrası özet olay sınırı, bellek kanallarının dolması,
veritabanı kesintisi, timeout, kalıcı HTTP hatası, artan retry, son denemede dahi
429 ortak beklemesi, worker kapanışı ve güvenli hata metinleri.

Giriş senaryoları aynı IP ve dağıtılmış IP'lerin aynı hesaba yönelmesini,
kilitli retleri, sayaç sınırını/süre sonunu, bilinmeyen IP'nin kaynak kanıtı
sayılmamasını ve mevcut proxy politikasını kapsar. API testleri auth, admin rolü,
CSRF/origin ve token/chat ID sızmamasını doğrular. SSH testleri başlangıç cursor'ı,
artımlı okuma argümanları, kayıt sınırı, erişim hatası ve ham hesap/log metninin
bildirim katmanına aktarılmamasını doğrular.

HTTP testleri enjekte edilmiş sahte `RoundTripper`, SSH testleri sahte komut
çalıştırıcısı kullanır. Gerçek Linux systemd/journal izinleri ve gerçek Telegram
bot/sohbet yetkileri bu macOS ortamında uçtan uca doğrulanmadı.

## Uyarlanabilir CPU bildirimi eklemesi

CPU, mevcut collector örneklerini kullanır; yeni bir host ölçümü veya ayrı
izleme işi eklenmedi. Aynı hostun son 24 saatlik geçerli düşük yük kayıtları
DuckDB'de medyanla özetlenir; son beş dakika dışarıda kalır. Referans en fazla
beş dakikada bir sorgulanır ve yükseliş/aktif olay boyunca dondurulur. Sabit
%35 tabanı, üç kat koşulu, %80 kritik ve %20 toparlanma sınırları sunucu
yapılandırmasından değiştirilebilir. Eski RAM/disk/swap/servis ve paneldeki
bir saatlik anomali davranışları korunur.

Eklenen testler: %35 mutlak taban ile göreli artışın birlikte aranması,
%80 bağımsız kritik sınırı, 5/2/3 dakikalık beklemeler, kısa yükselişin sönmesi,
uyarıdan kritiğe geçiş, tek toparlanma, sıfır CPU'nun geçerli veri olması,
eksik/NaN/aralık dışı/eski/yinelenen ölçümler, seyrek/yetersiz geçmiş,
veritabanı kesintisinde önbellek ve sabit eşik, sorgunun beş dakika
önbelleklenmesi, uzun yükselişte referansın değişmemesi, host ayrımı,
eski CPU alarmının tekrarlanmadan devralınması, restart'ta referans/olay/outbox
korunması, dolu kuyruk ve collector'ı bekletmeyen geçmiş sorgusu.

Yeni SQL testi gerçek geçici DuckDB kullanır; farklı host, son beş dakika,
24 saat dışı, NULL, NaN, sonsuz ve yüksek CPU değerlerinin normal referansa
karışmadığını doğrular. Panel testi, eksik referansın %0 gibi gösterilmemesini
ve donmuş referans/eşik bilgilerinin görünmesini doğrular.

Tam `go test -race ./...` ve son değişiklikler için ilgili paketlerin yarış
kontrolleri başarılıdır. Bildirim/yapılandırma paketlerinin lint sonucu sıfır
bulgudur. `make check` test ve vet aşamalarını geçip yukarıdaki mevcut lint
bulgularında durdu; ayrıca tekrar çalıştırılan güvenlik taraması aynı beş Go
standart kitaplığı bulgusunu raporladı. Çalışan servis veya veritabanı değiştirilmedi.

Referans sorgusunun mikro ölçümü: Apple M4, macOS arm64, Go 1.26.5, geçici gerçek
DuckDB; 30 saniyelik 86.400 sentetik kayıt (30 gün), tek host, aralıklı %90
sıçramalar ve diğer örneklerde %10–16 CPU. Ölçülen sorgu son 24 saatten düşük yük
medyanını okur. Veri oluşturma ve migration ölçüm dışında; race kapalıdır.
Go bellek ölçümü DuckDB'nin native bellek kullanımını içermez. Bu deney tüm
Sentinel sürecinin CPU/RAM tüketimi veya üretim tahmini değildir.

Üç tekrarda ortalama sorgu süreleri **0,618 ms**, **0,649 ms**, **0,582 ms**
olarak ölçüldü. Sorgu başına Go ayırmaları 3.707–3.712 bayt ve 107 ayırmadır;
bu sayılar tüm süreç RSS'si veya DuckDB native bellek tüketimi değildir.

```sh
go test ./internal/store -run '^$' -bench '^BenchmarkCPUReference24Hours$' -benchtime=2s -count=3
```

## Kaynak ölçüm yöntemi

Aşağıdaki üç süreç ölçümü ilk Telegram eklemesine aittir; uyarlanabilir CPU
referans sorgusunun maliyeti yukarıdaki ayrı deneyle değerlendirilir.

Ölçüm ortamı: aynı macOS arm64 makine, Go 1.26.5; race kapalı derlenmiş aynı test
binary'si, geçici gerçek DuckDB, sahte host snapshot'ı ve başarılı yanıt veren
sahte HTTP transport. SSH/systemd subprocess çağrıları ve planlı özetler kapalı.
Her senaryo ayrı süreçte, sırayla bir kez, 10 saniye çalıştırıldı. Ölçüm aracı
macOS `/usr/bin/time -l`; CPU süresi user + sys toplamı, RAM ise maximum resident
set size (tepe RSS). Süreç başlatma, DuckDB migration ve ortak 10 ms test üreticisi
zamanlayıcısı ölçüme dahildir.

- **Kapalı:** Telegram çalışanı yok; yalnızca ortak test altyapısı çalışır.
- **Açık/boşta:** koordinatör ve gönderim çalışanı, tek başlangıç metriği; alarm yok.
- **Yoğun:** yaklaşık 1000 reddedilen giriş olayı/saniye, aynı hesap ve 2048 farklı
  geçerli IP arasında dönüşümlü akış. 1024 sayaç ve 128 outbox sınırları zorlanır;
  gerçek parola hash hesabı, ağ, TLS ve tam host collector bu deneyin parçası değildir.

Son çalıştırmanın ölçülen sonuçları:

- Kapalı: toplam CPU **0,12 s** (0,07 user + 0,05 sys), tepe RSS
  **38,83 MiB** (40.714.240 bayt), süreç duvar süresi 11,49 s.
- Açık/boşta: toplam CPU **0,11 s** (0,07 user + 0,04 sys), tepe RSS
  **48,92 MiB** (51.298.304 bayt), süreç duvar süresi 10,04 s.
- Yoğun: toplam CPU **0,20 s** (0,14 user + 0,06 sys), tepe RSS
  **59,34 MiB** (62.226.432 bayt), süreç duvar süresi 10,04 s.

Üçü de başarıyla tamamlandı. CPU süreleri aracın iki ondalık çözünürlüğündedir;
boşta ölçümün kapalıdan 0,01 s düşük olması performans iyileşmesi kanıtı değildir.

Bu kısa, tek tekrarlı alt sistem deneyi üretim CPU/RAM tahmini değildir. Tepe RSS,
Go/DuckDB ayırıcıları, başlangıç yükü ve işletim sistemi önbelleklerinden etkilenir;
steady-state bellek veya gerçek sunucu tüketimi olarak yorumlanmamalıdır.

Yeniden üretmek için (gerçek Telegram çağrısı yapmaz):

```sh
go test -c -o /tmp/sentinel-notify-probe.test ./internal/notify
for mode in disabled idle storm; do
  env SENTINEL_NOTIFY_PROBE="$mode" /usr/bin/time -l \
    /tmp/sentinel-notify-probe.test -test.run '^TestNotificationResourceProbe$' -test.count=1 -test.v
done
```

Normal test çalıştırmalarında bu kaynak deneyi atlanır; yalnızca belirtilen ortam
değişkeni ile çalışır. Bağımsız bir servis veya sürekli benchmark işi oluşturulmaz.
