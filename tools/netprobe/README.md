# netprobe

Замер достижимости mase-сервера из текущей сети (Ф1 карты). Один файл, зависимость — только `gorilla/websocket`.

```bash
cd tools/netprobe
go build -o netprobe .

# пробный аккаунт: лучше отдельный, сервер держит одно соединение на пользователя
export MASE_PROBE_PHONE=+7XXXXXXXXXX MASE_PROBE_PASSWORD='...'
./netprobe -url wss://mase.nemilk.ru/ws -label "МТС, мобильный, Ростов, Cloudflare" -register   # первый раз
./netprobe -url wss://<адрес-пробного-VPS>/ws -label "МТС, мобильный, Ростов, VPS-timeweb"
```

Шаги: `dns`, `tcp`, `ws (TLS+handshake)`, `auth.login`/`auth.register`, `ping`, лестница кадров `ws кадр 4/16/64/128/256 КБ` (отличает «режет по размеру кадра» от «режет по общему объёму»; первый провал останавливает прогон), `ws upstream 1 МБ` (четыре кадра по 256 КБ: сервер закрывает соединение на кадрах больше 512 КБ), `https upload/download 5 МБ` (свой файл через `/upload` и `/media/`, сверка sha256), `hold 30m` (ping раз в 30 с). Код возврата 1, если любой шаг провалился.

Ключи: `-hold 0` без удержания, `-http-mb 0` без HTTPS-шагов, `-register` создать пробный аккаунт.

Побочные эффекты на сервере: один токен сессии на запуск и один «осиротевший» файл 5 МБ в `media/` (удаления через API нет). Телефон и пароль читаются только из окружения. Результаты заносить в `docs/reachability.md`.

## Сохранение вывода

Вывод не содержит ни пароля, ни токена, ни телефона (только адрес сервера и метку), его можно класть в репозиторий. Из корня репозитория, с `pipefail`, чтобы код возврата netprobe не терялся за `tee`:

```bash
mkdir -p docs/reachability-runs
set -o pipefail
LABEL="мтс-мобильный-cloudflare"   # оператор-тип-путь, без пробелов
./tools/netprobe/netprobe -url wss://mase.nemilk.ru/ws -label "$LABEL" 2>&1 \
  | tee "docs/reachability-runs/$(date +%F_%H%M)-$LABEL.txt"
echo "код возврата netprobe: $?"
```

`tee` пишет и в консоль, и в файл. Строку в таблицу `docs/reachability.md` заносить по этому файлу, а не по памяти.
