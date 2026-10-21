# olcWRT

Клиент [olcRTC](https://github.com/openlibrecommunity/olcrtc) для роутеров на OpenWrt.

olcRTC — зашифрованный TCP-туннель поверх WebRTC, который выглядит как обычный видеозвонок в Jitsi, Яндекс Телемосте или WB Stream. olcWRT запускает клиент olcRTC прямо на роутере и заворачивает в туннель трафик устройств локальной сети — без настройки самих устройств.

```text
устройства LAN ──► роутер OpenWrt ──► olcrtc (клиент) ──► Jitsi / Telemost / WB Stream ──► olcrtc srv ──► интернет
                   nftables REDIRECT    SOCKS5 127.0.0.1:8808
                   dnsmasq ──► DNS olcWRT ──► DNS через туннель
```

## Возможности

- Все провайдеры и транспорты olcRTC: `jitsi`, `telemost`, `wbstream`; `datachannel`, `vp8channel`, `seichannel`, `videochannel`.
- Импорт ссылок `olcrtc://` и подписок (формат `sub.md` из olcRTC, а также base64 и ссылки `olcbox://add?url=…`).
- Автообновление подписок (`#refresh`, заголовки `profile-update-interval`, `subscription-userinfo`, идентификация как у olcbox: `x-hwid`).
- Несколько серверов с автоматическим переключением (failover-профили olcrtc).
- Прозрачное проксирование TCP для LAN (nftables), IPv4 и IPv6.
- Режимы: весь трафик через туннель / только из списка / только SOCKS5.
- Списки доменов и подсетей (свои и загружаемые по URL): «через туннель» и «исключения». Домены работают без `dnsmasq-full`.
- DNS через туннель (DNS-over-TCP через сервер olcrtc), защита от подмены DNS и утечек.
- Фильтр клиентов по MAC/IP, блокировка QUIC, перехват DNS клиентов со своими DNS-серверами.
- SOCKS5-прокси для локальной сети (с логином и паролем).
- Проверка соединения и тест отдельного сервера (временным экземпляром olcrtc).
- Веб-интерфейс LuCI на русском и английском.
- Работает на OpenWrt 23.05 / 24.10 (opkg, `.ipk`) и 25.12 (apk, `.apk`).

## Требования

| | |
|---|---|
| OpenWrt | 23.05 или новее, с `firewall4` (nftables) — это стандарт с 22.03 |
| Флеш | ~45 МБ свободного места (olcrtc ≈ 31 МБ, olcwrt ≈ 7 МБ в распакованном виде) |
| ОЗУ | от 128 МБ (olcrtc ≈ 35 МБ, olcwrt ≈ 8 МБ RSS) |
| Сервер | работающий `olcrtc` в режиме `srv` (см. [документацию olcRTC](https://github.com/openlibrecommunity/olcrtc/blob/master/docs/fast.ru.md)) |

На роутерах с 16–32 МБ флеша olcrtc не поместится: нужен extroot (USB-накопитель).

## Установка

1. Узнайте архитектуру роутера:

   ```sh
   grep DISTRIB_ARCH /etc/openwrt_release
   ```

2. Соберите пакеты под неё (см. [Сборка](#сборка)) или возьмите готовые из `dist/`.

3. Скопируйте пакеты на роутер (например, в `/tmp`) и установите.

   OpenWrt 23.05 / 24.10:

   ```sh
   opkg install /tmp/olcrtc_*.ipk /tmp/olcwrt_*.ipk /tmp/luci-app-olcwrt_*.ipk /tmp/luci-i18n-olcwrt-ru_*.ipk
   ```

   OpenWrt 25.12:

   ```sh
   apk add --allow-untrusted /tmp/olcrtc-*.apk /tmp/olcwrt-*.apk /tmp/luci-app-olcwrt-*.apk /tmp/luci-i18n-olcwrt-ru-*.apk
   ```

4. Откройте LuCI → **Службы → olcWRT**.

## Настройка через LuCI

1. **Серверы → Импорт ссылок…** — вставьте ссылку `olcrtc://…` (её выдаёт `install.sh` olcRTC на сервере) или URL подписки. Сервер можно добавить и вручную: провайдер, транспорт, комната и ключ должны совпадать с серверной стороной.
2. **Обзор → Включить и запустить.** Статус сменится на «Подключено», когда клиент встретится с сервером в комнате.
3. **Проверить соединение** — запрос через туннель, показывает задержку и внешний IP.

Страницы:

| Страница | Что там |
|---|---|
| Обзор | состояние туннеля, активный сервер, трафик, DNS, быстрая смена сервера, проверка |
| Серверы | список серверов, порядок переключения, импорт/экспорт ссылок, проверка отдельного сервера |
| Подписки | URL подписок, интервал обновления, состояние, «Обновить» |
| Маршрутизация | режим, интерфейсы, клиенты, списки доменов и подсетей, DNS |
| Настройки | включение, журнал, DNS для olcrtc, SOCKS5 для LAN, порты, лимит памяти |
| Журнал | сообщения olcWRT и olcrtc |

## Режимы маршрутизации

| Режим | Что идёт через туннель |
|---|---|
| **Весь трафик** (по умолчанию) | весь TCP клиентов, кроме частных сетей и списка исключений |
| **Только из списка** | TCP к доменам и подсетям из списка проксирования |
| **Выключено** | ничего не перенаправляется, работает только SOCKS5-прокси `127.0.0.1:8808` (или в LAN с паролем) |

Списки (страница «Маршрутизация → Списки» или файлы `/etc/olcwrt/proxy.lst`, `/etc/olcwrt/bypass.lst`) — по одной записи в строке:

```text
example.com        # домен вместе со всеми поддоменами
203.0.113.0/24     # подсеть IPv4 или IPv6
198.51.100.7       # адрес
```

Списки можно подключать по URL (обновляются раз в сутки): простые текстовые списки доменов/подсетей, понимаются и строки dnsmasq `server=/…/` и `nftset=/…/`.

### Как работают домены

olcWRT встраивается в dnsmasq роутера (через его `conf-dir`, `/etc/config/dhcp` не меняется):

- запросы к доменам из списков dnsmasq передаёт резолверу olcWRT (`127.0.0.1:18853`);
- домены из списка проксирования разрешаются **через туннель**, их адреса попадают в набор nftables `proxy4_dns`/`proxy6_dns`;
- домены-исключения разрешаются напрямую, адреса попадают в `bypass4_dns`/`bypass6_dns`;
- в режиме «весь трафик» с опцией «Разрешать все имена через туннель» через туннель разрешаются вообще все имена (кроме исключений).

Адрес добавляется в набор до того, как клиент получит ответ DNS, поэтому уже первое соединение идёт правильным маршрутом. Поэтому `dnsmasq-full` не нужен.

## Ограничения

- olcRTC передаёт **только TCP**. UDP (кроме DNS) идёт напрямую; QUIC (UDP/443) к проксируемым адресам по умолчанию блокируется, чтобы браузеры перешли на TCP.
- Трафик самого роутера (opkg, NTP и т.п.) в туннель не заворачивается. Подписки и списки olcWRT загружает напрямую, а при неудаче — через туннель.
- Если туннель недоступен, проксируемый трафик не идёт в обход (поведение «kill switch»). Чтобы вернуть прямой доступ, остановите службу.
- olcrtc находит сервис видеозвонков через DNS провайдера (`auto`) или указанный в настройках сервер — он не должен указывать на сам роутер.

## Командная строка

```sh
olcwrt status                       # состояние (JSON)
olcwrt import 'olcrtc://…'          # импорт ссылок (можно несколько строк, файл или "-" для stdin)
olcwrt import https://example.org/sub   # добавить и загрузить подписку
olcwrt sub update [id…]             # обновить подписки
olcwrt lists update                 # загрузить списки по URL
olcwrt check [server-id]            # проверить туннель или отдельный сервер
olcwrt export [server-id…]          # ссылки olcrtc:// для серверов
olcwrt genconf                      # сгенерированный YAML для olcrtc
olcwrt reload                       # применить конфигурацию (дожидается применения)
/etc/init.d/olcwrt start|stop|restart
```

Пример настройки без LuCI:

```sh
olcwrt import 'olcrtc://jitsi?datachannel@https://meet.example.org/myroom#<64 hex>$Мой сервер'
uci set olcwrt.main.enabled=1
uci commit olcwrt
/etc/init.d/olcwrt start
```

## Конфигурация

Всё хранится в UCI `/etc/config/olcwrt`:

| Секция | Параметры |
|---|---|
| `main` | `enabled`, `server` (список — порядок переключения), `dns_server`, `socks_port`, `redir_port`, `dns_port`, `lan_socks`, `socks_user`, `socks_pass`, `failover_retry`, `mem_limit`, `log_level`, `check_url` |
| `routing` | `mode` (`all`/`list`/`off`), `interface`, `ipv6`, `block_quic`, `dns_hijack`, `source_mode` (`all`/`include`/`exclude`), `source`, `proxy_list_url`, `bypass_list_url` |
| `dns` | `enabled`, `tunnel`, `upstream`, `direct`, `filter_aaaa`, `set_timeout` |
| `server` | `label`, `provider`, `transport`, `room`, `key`, `token`, `channel`, `subscription`, параметры транспорта (`vp8_fps`, `vp8_batch`, `sei_*`, `video_*`) |
| `subscription` | `label`, `url`, `enabled`, `interval`, `via_tunnel`, `request_mode` |

Прочие файлы:

| Путь | Назначение |
|---|---|
| `/etc/olcwrt/proxy.lst`, `/etc/olcwrt/bypass.lst` | пользовательские списки |
| `/etc/olcwrt/lists/` | загруженные списки |
| `/etc/olcwrt/subs/` | состояние подписок |
| `/etc/olcwrt/hwid` | постоянный идентификатор для подписок |
| `/var/run/olcwrt/` | состояние демона, конфигурация olcrtc, правила nft |
| `/var/log/olcwrt.log` | журнал (в ОЗУ, до 256 КБ + ротация) |

Что olcWRT меняет в системе во время работы: таблица nftables `inet olcwrt` и файлы `olcwrt.conf`, `.olcwrt.servers` в `conf-dir` dnsmasq. При остановке всё удаляется.

## Сборка

### Без SDK (рекомендуется)

Нужен Go 1.26+ и исходники olcRTC (по умолчанию `SOURCES/olcrtc`).

```sh
cd tools/pkgbuild
go build -o pkgbuild .
./pkgbuild -arch mipsel_24kc,aarch64_cortex-a53     # .ipk для OpenWrt 23.05/24.10
./pkgbuild -arch common                               # набор популярных архитектур
./pkgbuild archs                                      # список поддерживаемых архитектур
```

Пакеты появятся в `dist/ipk/<архитектура>/` и `dist/ipk/all/`.

Для OpenWrt 25.12 (`.apk`) нужен `apk` из apk-tools 3. Проще всего взять статический бинарник из Alpine (`apk-tools-static`, файл `sbin/apk.static`):

```sh
./pkgbuild -arch x86_64 -apk /path/to/apk.static          # Linux (используется fakeroot)
pkgbuild.exe -arch x86_64 -apk wsl:/root/apk.static       # Windows: apk.static запускается в WSL
```

Параметры: `-olcrtc <путь>` — другие исходники olcRTC, `-no-olcrtc` — без пакета olcrtc, `-upx` — сжать бинарники upx, `-out <каталог>`.

Перевод: после изменения строк интерфейса выполните `./pkgbuild pot` — обновится шаблон, а в `po/ru/olcwrt.po` допишутся непереведённые строки.

### В OpenWrt SDK / buildroot

Каталог `feed/` — обычный фид OpenWrt с пакетами `olcrtc`, `olcwrt` и `luci-app-olcwrt`:

```sh
echo "src-link olcwrt /path/to/olcwrt/feed" >> feeds.conf.default
./scripts/feeds update -a
./scripts/feeds install olcrtc olcwrt luci-app-olcwrt
make menuconfig    # Network → VPN → olcwrt, LuCI → Applications → luci-app-olcwrt
make package/olcrtc/compile package/olcwrt/compile package/luci-app-olcwrt/compile
```

olcRTC требует Go 1.26: он есть в фиде packages OpenWrt 25.12. В SDK 24.10 замените `feeds/packages/lang/golang` версией из ветки 25.12 или master. Пакет `olcrtc` скачивает исходники с GitHub по зафиксированному коммиту (`PKG_SOURCE_VERSION` в `feed/olcrtc/Makefile`).

## Разработка

```text
feed/olcwrt/src/        демон и CLI olcwrt (Go, только стандартная библиотека)
feed/olcwrt/files/      init-скрипт, конфигурация по умолчанию, списки
feed/olcrtc/            пакет olcrtc
feed/luci-app-olcwrt/   интерфейс LuCI (JavaScript), перевод, ACL, rpcd-плагин
tools/pkgbuild/         сборщик .ipk/.apk без SDK (+ po → lmo)
tools/vm/               тестовый стенд: OpenWrt x86-64 в QEMU с LAN-клиентом
```

Тесты: `cd feed/olcwrt/src && go test ./...`.

Тестовый стенд (Linux или WSL, от root): `tools/vm/owrt-vm.sh image openwrt-…-x86-64-generic-ext4-combined.img.gz`, затем `owrt-vm.sh start`. Роутер доступен по `ssh` на 192.168.1.1, LAN-клиент — сетевое пространство `lanc` (`owrt-vm.sh lan <команда>`), LuCI — `owrt-vm.sh forward 8081`. `tools/vm/netcheck.py` — простые HTTP- и DNS-проверки с клиента.

### Устройство

`olcwrt run` (запускается procd) — один процесс, который:

- генерирует YAML для olcrtc из UCI и запускает `olcrtc` дочерним процессом, перезапускает его при падении, определяет состояние туннеля по его журналу и не пишет в журнал строки о каждом соединении (они остаются только на уровне «Отладка»);
- принимает перенаправленные nftables соединения (`SO_ORIGINAL_DST`) и отправляет их на исходный адрес через SOCKS5 olcrtc;
- обслуживает DNS для dnsmasq: туннельные запросы идут по одному конвейерному TCP-соединению через туннель, остальные — напрямую;
- применяет таблицу nftables и настройки dnsmasq, обновляет подписки и списки по расписанию;
- по SIGHUP (`olcwrt reload`, «Сохранить и применить» в LuCI) применяет новую конфигурацию, не переподключая туннель, если настройки olcrtc не изменились.

LuCI общается с демоном через rpcd-плагин `luci.olcwrt` (тот же бинарник `olcwrt`); долгие операции (обновление подписок, проверки) выполняются фоновыми задачами.

## Лицензия

WTFPL, как и olcRTC.
