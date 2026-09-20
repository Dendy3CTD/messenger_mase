#!/usr/bin/env bash
# Сценарий «Готово когда» Ф3 одним запуском: миграции на КОПИИ старой БД и на пустой БД.
# К исходной БД не прикасается: копия снимается средствами SQLite (.backup), сервер и все
# проверки работают только с копией во временном каталоге, который удаляется в конце.
#
#   ops/phase3-scenario.sh                       # источник по умолчанию ~/mase-data/dev.sqlite
#   SCEN_SRC_DB=/путь/к/другой.sqlite ops/phase3-scenario.sh
#
# Для шага «вход по старому паролю» нужны телефон и пароль СУЩЕСТВУЮЩЕГО пользователя этой БД
# (SCEN_PHONE и SCEN_PASSWORD в окружении, иначе скрипт спросит; пароль не печатается).
# Без них этот шаг пропускается и в итоге помечается как НЕ ПРОЙДЕН.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="${SCEN_SRC_DB:-$HOME/mase-data/dev.sqlite}"
PORT="${SCEN_PORT:-18100}"
export PATH="$PATH:$HOME/go/bin:/usr/local/go/bin"
export GOPATH="${GOPATH:-$HOME/go-workspace}"

PASS=0; FAILN=0; SKIP=0
say()  { printf '\n== %s\n' "$*"; }
ok()   { printf '   ok    %s\n' "$*"; PASS=$((PASS+1)); }
fail() { printf '   FAIL  %s\n' "$*"; FAILN=$((FAILN+1)); }
skip() { printf '   ПРОПУЩЕНО  %s\n' "$*"; SKIP=$((SKIP+1)); }

die() { printf 'ОШИБКА: %s\n' "$*" >&2; exit 2; }
command -v sqlite3 >/dev/null || die "нужен sqlite3 (sudo apt install sqlite3)"
command -v go >/dev/null || die "go не найден в PATH"
[[ -f "$SRC" ]] || die "нет исходной БД: $SRC"
case "$(realpath "$SRC")" in
  "$(realpath -m "$HOME/mase-data/mase.sqlite")") die "исходная БД — прод ($SRC); сценарий запускается только на dev или своей копии" ;;
esac

TMP="$(mktemp -d)"
SERVER_PID=""
cleanup() {
  [[ -n "$SERVER_PID" ]] && kill "$SERVER_PID" 2>/dev/null && wait "$SERVER_PID" 2>/dev/null
  rm -rf "$TMP"
}
trap cleanup EXIT INT TERM

say "0. сборка сервера и netprobe во временный каталог"
( cd "$ROOT/go-server" && go build -buildvcs=false -o "$TMP/mase-server" ./cmd/server ) && ok "сервер собран" || die "сервер не собрался"
( cd "$ROOT/tools/netprobe" && GOFLAGS=-mod=mod go build -o "$TMP/netprobe" . ) && ok "netprobe собран" || die "netprobe не собрался"

say "1. копия исходной БД средствами SQLite (.backup), исходная не трогается"
COPY="$TMP/copy.sqlite"
sqlite3 "$SRC" ".backup '$COPY'" && ok "копия снята: $(stat -c %s "$COPY") байт" || die ".backup не удался"
counts() { sqlite3 -readonly "$1" "select 'users='||count(*) from users; select 'tokens='||count(*) from tokens; select 'messages='||count(*) from messages; select 'chats='||count(*) from chats; select 'groups='||count(*) from groups;" | tr '\n' ' '; }
BEFORE="$(counts "$COPY")"; echo "   до: $BEFORE"

say "2. migrate status до миграции"
OUT="$("$TMP/mase-server" migrate status -db "$COPY" 2>&1)"; echo "$OUT" | sed 's/^/   /'
if grep -q 'pending' <<<"$OUT"; then ok "00001 ожидает применения (БД без версии)"
elif grep -q 'applied' <<<"$OUT"; then ok "00001 уже применена (БД уже мигрирована; шаг up ниже проверит идемпотентность)"
else fail "статус не распознан"; fi

say "3. migrate up: применяет baseline и делает копию перед миграцией"
OUT="$("$TMP/mase-server" migrate up -db "$COPY" 2>&1)"; RC=$?; echo "$OUT" | sed 's/^/   /'
[[ $RC -eq 0 ]] && grep -q 'applied' <<<"$OUT" && ok "up прошёл, версия 1 applied" || fail "up (код $RC)"
NB=$(ls "$COPY".pre-migrate-* 2>/dev/null | wc -l)
echo "   копий перед миграцией: $NB"
if grep -q '\[DB\] копия перед миграцией' <<<"$OUT"; then
  ok "перед миграцией создана копия ($NB файл.)"
  B="$(ls "$COPY".pre-migrate-* | head -1)"
  [[ "$(sqlite3 -readonly "$B" 'pragma integrity_check')" == ok ]] && ok "копия целая (integrity_check ok)" || fail "копия перед миграцией повреждена"
else
  ok "копия не понадобилась (БД пустая или миграция уже была применена)"
fi

say "4. migrate up повторно: ничего не делает, новых копий нет"
"$TMP/mase-server" migrate up -db "$COPY" >/dev/null 2>&1; RC=$?
[[ $RC -eq 0 && "$(ls "$COPY".pre-migrate-* 2>/dev/null | wc -l)" -eq "$NB" ]] && ok "повторный up без изменений" || fail "повторный up (код $RC)"

say "5. migrate down: откат ниже baseline запрещён"
OUT="$("$TMP/mase-server" migrate down -db "$COPY" 2>&1)"; RC=$?
[[ $RC -ne 0 ]] && grep -q 'baseline' <<<"$OUT" && ok "down отказал: ниже baseline нельзя" || fail "down не отказал (код $RC): $OUT"

say "6. целостность и данные"
[[ "$(sqlite3 -readonly "$COPY" 'pragma integrity_check')" == ok ]] && ok "integrity_check ok" || fail "integrity_check"
[[ -z "$(sqlite3 -readonly "$COPY" 'pragma foreign_key_check')" ]] && ok "foreign_key_check пуст" || fail "foreign_key_check не пуст"
AFTER="$(counts "$COPY")"; echo "   после: $AFTER"
[[ "$BEFORE" == "$AFTER" ]] && ok "число строк не изменилось" || fail "число строк изменилось"

start_server() { # $1 = БД
  MASE_ADDR="127.0.0.1:$PORT" MASE_DB="$1" MASE_MEDIA="$TMP/media" "$TMP/mase-server" >"$TMP/server.log" 2>&1 &
  SERVER_PID=$!
  for _ in $(seq 1 30); do curl -fsS "http://127.0.0.1:$PORT/health" >/dev/null 2>&1 && return 0; sleep 0.3; done
  return 1
}
stop_server() { [[ -n "$SERVER_PID" ]] && kill "$SERVER_PID" 2>/dev/null && wait "$SERVER_PID" 2>/dev/null; SERVER_PID=""; }

say "7. сервер поднимается на копии старой БД, вход по старому паролю"
if start_server "$COPY"; then ok "сервер стартовал на копии"; else fail "сервер не стартовал: $(tail -3 "$TMP/server.log")"; fi
PHONE="${SCEN_PHONE:-}"; PW="${SCEN_PASSWORD:-}"
if [[ -z "$PHONE" && -t 0 ]]; then read -rp "   телефон существующего пользователя (Enter — пропустить шаг): " PHONE; fi
if [[ -n "$PHONE" && -z "$PW" && -t 0 ]]; then read -rsp "   пароль: " PW; echo; fi
if [[ -n "$PHONE" && -n "$PW" ]]; then
  MASE_PROBE_PHONE="$PHONE" MASE_PROBE_PASSWORD="$PW" "$TMP/netprobe" -url "ws://127.0.0.1:$PORT/ws" -hold 0 -http-mb 1 >"$TMP/np.txt" 2>&1
  if grep -q '^ok   auth.login' "$TMP/np.txt"; then ok "вход по старому паролю прошёл (netprobe: auth.login)"; else fail "вход не прошёл:"; sed 's/^/     /' "$TMP/np.txt" | grep -v '^     netprobe'; fi
else
  skip "нет телефона и пароля существующего пользователя (SCEN_PHONE/SCEN_PASSWORD)"
fi
stop_server

say "8. сервер на пустой БД"
if start_server "$TMP/empty.sqlite"; then ok "сервер стартовал на пустой БД"; else fail "сервер не стартовал: $(tail -3 "$TMP/server.log")"; fi
stop_server
[[ "$(sqlite3 -readonly "$TMP/empty.sqlite" 'select max(version_id) from goose_db_version where is_applied')" == 1 ]] && ok "на пустой БД применена версия 1" || fail "версия на пустой БД"
[[ "$(sqlite3 -readonly "$TMP/empty.sqlite" 'pragma integrity_check')" == ok && -z "$(sqlite3 -readonly "$TMP/empty.sqlite" 'pragma foreign_key_check')" ]] && ok "пустая БД: integrity_check ok, foreign_key_check пуст" || fail "пустая БД: проверки целостности"

say "ИТОГ: ok=$PASS, FAIL=$FAILN, пропущено=$SKIP"
if [[ $FAILN -gt 0 ]]; then echo "СЦЕНАРИЙ НЕ ПРОЙДЕН"; exit 1; fi
if [[ $SKIP -gt 0 ]]; then echo "СЦЕНАРИЙ ПРОЙДЕН НЕ ПОЛНОСТЬЮ: были пропущенные шаги"; exit 3; fi
echo "СЦЕНАРИЙ ПРОЙДЕН"
