#!/usr/bin/env bash
# Проверки run.sh без запуска сервера: только MASE_DRY_RUN=1 и временный каталог данных.
# Реальный ~/mase-data и прод-БД не используются.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN="$HERE/run.sh"

# Защита: без поддержки сухого запуска run.sh может реально собрать и запустить сервер и туннель.
if ! grep -q 'MASE_DRY_RUN' "$RUN"; then
  echo "ОТКАЗ: $RUN не поддерживает MASE_DRY_RUN — тесты не запускаются, чтобы не стартовать сервер и туннель." >&2
  exit 3
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Заглушки: даже при ошибке в run.sh ничего реального не соберётся и не запустится.
mkdir -p "$TMP/shim"
printf '#!/bin/sh\necho "test shim: go запрещён в тестах" >&2\nexit 99\n' > "$TMP/shim/go"
printf '#!/bin/sh\necho "test shim: cloudflared запрещён в тестах" >&2\nexit 99\n' > "$TMP/shim/cloudflared"
chmod +x "$TMP/shim/go" "$TMP/shim/cloudflared"
export PATH="$TMP/shim:$PATH"
export CLOUDFLARED_BIN="$TMP/shim/cloudflared"

export MASE_DATA_DIR="$TMP/data"
mkdir -p "$MASE_DATA_DIR"
touch "$MASE_DATA_DIR/mase.sqlite"      # «прод-БД» внутри временного каталога

pass=0; fail=0
OUT=""; CODE=0

# run_case ожидаемый_код "подстрока в выводе|" описание -- [env ...] аргументы run.sh
check() { # $1=описание $2=ожидаемый код $3=подстрока (или "") ; результат в OUT/CODE
  local desc="$1" want="$2" needle="$3"
  if [[ "$CODE" == "$want" && ( -z "$needle" || "$OUT" == *"$needle"* ) ]]; then
    pass=$((pass+1)); printf '  ok   %s\n' "$desc"
  else
    fail=$((fail+1)); printf '  FAIL %s\n       код=%s (ожидался %s), ожидалась подстрока: %q\n       вывод: %s\n' \
      "$desc" "$CODE" "$want" "$needle" "$OUT"
  fi
}
exec_run() { OUT="$(timeout 20 env -u MASE_DB -u MASE_ADDR -u MASE_MEDIA -u MASE_DEV_DB -u MASE_DEV_PORT -u MASE_ALLOW_NEW_DB -u MASE_I_MEAN_PROD "$@" 2>&1)"; CODE=$?; }

echo "аргументы"
exec_run bash "$RUN";                                   check "без аргументов — ошибка использования" 2 "Использование"
exec_run bash "$RUN" staging;                           check "неизвестный режим — ошибка"            2 "Использование"
exec_run bash "$RUN" dev prod;                          check "лишний аргумент — ошибка"              2 "Использование"

echo "prod"
exec_run env MASE_DRY_RUN=1 MASE_I_MEAN_PROD=1 bash "$RUN" prod;  check "prod без MASE_DB не стартует"          1 "MASE_DB"
exec_run env MASE_DRY_RUN=1 MASE_I_MEAN_PROD=1 MASE_DB="$TMP/nope.sqlite" bash "$RUN" prod
check "prod с несуществующим файлом БД не стартует"     1 "MASE_ALLOW_NEW_DB"
exec_run env MASE_DRY_RUN=1 MASE_I_MEAN_PROD=1 MASE_DB="$TMP/nope.sqlite" MASE_ALLOW_NEW_DB=1 bash "$RUN" prod
check "prod с MASE_ALLOW_NEW_DB=1 допускает новую БД"   0 "$TMP/nope.sqlite"
exec_run env MASE_DRY_RUN=1 MASE_I_MEAN_PROD=1 MASE_DB="$MASE_DATA_DIR/mase.sqlite" bash "$RUN" prod
check "prod с существующей БД: режим, порт, туннель"    0 "режим: prod"
check "prod использует порт 8080"                       0 ":8080"
check "prod запускает туннель"                          0 "туннель: да"
[[ ! -e "$TMP/nope.sqlite" ]] && { pass=$((pass+1)); echo "  ok   сухой запуск не создал файл БД"; } || { fail=$((fail+1)); echo "  FAIL сухой запуск создал БД"; }

echo "prod: защита от случайного запуска (MASE_I_MEAN_PROD=1 и интерактивный терминал)"
exec_run env MASE_DRY_RUN=1 MASE_DB="$MASE_DATA_DIR/mase.sqlite" bash "$RUN" prod
check "prod без MASE_I_MEAN_PROD отказывает"                 1 "MASE_I_MEAN_PROD"
exec_run env MASE_DRY_RUN=1 MASE_I_MEAN_PROD=0 MASE_DB="$MASE_DATA_DIR/mase.sqlite" bash "$RUN" prod
check "MASE_I_MEAN_PROD=0 не считается подтверждением"       1 "MASE_I_MEAN_PROD"
exec_run env MASE_DRY_RUN=1 MASE_I_MEAN_PROD=yes MASE_DB="$MASE_DATA_DIR/mase.sqlite" bash "$RUN" prod
check "MASE_I_MEAN_PROD=yes не считается подтверждением"     1 "MASE_I_MEAN_PROD"
exec_run env MASE_DRY_RUN=1 MASE_I_MEAN_PROD=1 MASE_DB="$MASE_DATA_DIR/mase.sqlite" bash "$RUN" prod
check "сухой запуск prod не требует терминала"               0 "режим: prod"

# Реальный (не сухой) запуск без терминала: должен отказать ДО сборки. Заглушка go — страховка.
exec_run env MASE_I_MEAN_PROD=1 MASE_DB="$MASE_DATA_DIR/mase.sqlite" bash "$RUN" prod </dev/null
check "prod без терминала (stdin не tty) отказывает"         1 "терминал"
[[ "$OUT" != *"test shim"* ]] && { pass=$((pass+1)); echo "  ok   отказ случился до сборки (заглушка go не вызывалась)"; } || { fail=$((fail+1)); echo "  FAIL защита сработала слишком поздно: дошло до сборки"; }

# Положительный контроль: в настоящем терминале защита пропускает и доходит до заглушки go.
if command -v script >/dev/null 2>&1; then
  OUT="$(timeout 20 script -qec "env MASE_I_MEAN_PROD=1 MASE_DB='$MASE_DATA_DIR/mase.sqlite' bash '$RUN' prod" /dev/null 2>&1 </dev/null)"; CODE=$?
  if [[ "$OUT" == *"test shim: go"* ]]; then pass=$((pass+1)); echo "  ok   в псевдотерминале защита пропускает (дошло до заглушки go)"
  else fail=$((fail+1)); echo "  FAIL в псевдотерминале защита не пропустила: код=$CODE вывод: $OUT"; fi
else
  echo "  --   пропущено: нет команды script (положительный контроль в терминале не проверен)"
fi

echo "dev"
exec_run env MASE_DRY_RUN=1 bash "$RUN" dev
check "dev: БД по умолчанию dev.sqlite"                 0 "$MASE_DATA_DIR/dev.sqlite"
check "dev: порт 8081"                                  0 ":8081"
check "dev: без туннеля"                                0 "туннель: нет"
[[ ! -e "$MASE_DATA_DIR/dev.sqlite" && ! -e "$MASE_DATA_DIR/dev-media" ]] \
  && { pass=$((pass+1)); echo "  ok   dev: сухой запуск ничего не создал"; } || { fail=$((fail+1)); echo "  FAIL dev: сухой запуск создал файлы"; }
exec_run env MASE_DRY_RUN=1 MASE_DB="$MASE_DATA_DIR/mase.sqlite" MASE_ADDR=":8080" MASE_MEDIA="/x" bash "$RUN" dev
check "dev игнорирует MASE_DB из окружения (не берёт прод-БД)" 0 "$MASE_DATA_DIR/dev.sqlite"
check "dev предупреждает, что MASE_DB игнорируется"     0 "игнорируется"
exec_run env MASE_DRY_RUN=1 MASE_DEV_DB="$MASE_DATA_DIR/mase.sqlite" bash "$RUN" dev
check "dev отказывается работать на прод-БД"            1 "прод"
exec_run env MASE_DRY_RUN=1 MASE_DEV_PORT=8090 bash "$RUN" dev
check "dev: порт настраивается MASE_DEV_PORT"           0 ":8090"

echo
echo "итого: $pass ок, $fail ошибок"
[[ "$fail" -eq 0 ]]
