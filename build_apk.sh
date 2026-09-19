#!/usr/bin/env bash
# ============================================================
#  Mase Messenger — сборка APK
#  Использование:
#    ./build_apk.sh              — собрать prod debug APK (com.mase.messenger, прод-сервер)
#    ./build_apk.sh dev          — собрать dev debug APK (com.mase.messenger.dev, локальный сервер)
#                                  для телефона: MASE_DEV_HOST=<IP машины в LAN> ./build_apk.sh dev
#    ./build_apk.sh release      — собрать prod release APK (неподписанный)
#    ./build_apk.sh install      — собрать prod debug и установить на подключённый телефон
#    ./build_apk.sh install-dev  — собрать dev debug и установить на подключённый телефон
# ============================================================
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ANDROID_DIR="$SCRIPT_DIR/android-client"
APK_DEBUG="$ANDROID_DIR/app/build/outputs/apk/prod/debug/app-prod-debug.apk"
APK_DEV="$ANDROID_DIR/app/build/outputs/apk/dev/debug/app-dev-debug.apk"
APK_RELEASE="$ANDROID_DIR/app/build/outputs/apk/prod/release/app-prod-release-unsigned.apk"
OUTPUT_DIR="$SCRIPT_DIR/dist"

MODE="${1:-debug}"

# ── Цвета ──────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; BOLD='\033[1m'; RESET='\033[0m'

info()    { echo -e "${BLUE}[INFO]${RESET}  $*"; }
success() { echo -e "${GREEN}[OK]${RESET}    $*"; }
warn()    { echo -e "${YELLOW}[WARN]${RESET}  $*"; }
error()   { echo -e "${RED}[ERROR]${RESET} $*"; exit 1; }

echo -e "${BOLD}╔══════════════════════════════╗${RESET}"
echo -e "${BOLD}║   Mase Messenger — Build     ║${RESET}"
echo -e "${BOLD}╚══════════════════════════════╝${RESET}"
echo ""

# ── Проверка local.properties ──────────────────────────────
LOCAL_PROPS="$ANDROID_DIR/local.properties"
if [ ! -f "$LOCAL_PROPS" ]; then
    warn "Файл local.properties не найден"
    # Попробовать найти ANDROID_HOME
    if [ -n "$ANDROID_HOME" ]; then
        echo "sdk.dir=$ANDROID_HOME" > "$LOCAL_PROPS"
        success "Создан local.properties: sdk.dir=$ANDROID_HOME"
    elif [ -d "$HOME/Android/Sdk" ]; then
        echo "sdk.dir=$HOME/Android/Sdk" > "$LOCAL_PROPS"
        success "Создан local.properties: sdk.dir=$HOME/Android/Sdk"
    else
        error "Не могу найти Android SDK. Создайте файл $LOCAL_PROPS с sdk.dir=<путь к SDK>"
    fi
fi

# ── Сборка APK ─────────────────────────────────────────────
case "$MODE" in
    debug|dev|release|install|install-dev) ;;
    *) error "Неизвестный режим: $MODE (debug | dev | release | install | install-dev)" ;;
esac

cd "$ANDROID_DIR"
mkdir -p "$OUTPUT_DIR"

TIMESTAMP=$(date +"%Y%m%d_%H%M%S")

# Адрес dev-сервера для реального телефона (для эмулятора не нужен: 10.0.2.2)
DEV_ARGS=()
if [ -n "${MASE_DEV_HOST:-}" ]; then
    DEV_ARGS+=("-PmaseDevHost=$MASE_DEV_HOST")
fi

# Установка на телефон: $1 = задача Gradle, $2 = путь к APK, $3 = applicationId; остальное — доп. аргументы Gradle
install_on_phone() {
    local task="$1" apk="$2" app_id="$3"
    shift 3
    if ! command -v adb &> /dev/null; then
        warn "adb не найден в PATH. Попытка найти в SDK..."
        ADB_PATH="$(cat "$LOCAL_PROPS" | grep sdk.dir | cut -d'=' -f2)/platform-tools/adb"
        if [ -x "$ADB_PATH" ]; then
            export PATH="$(dirname "$ADB_PATH"):$PATH"
        else
            error "adb не найден. Установите Android SDK Platform Tools"
        fi
    fi
    DEVICES=$(adb devices | grep -c "device$" || true)
    if [ "$DEVICES" -eq 0 ]; then
        error "Нет подключённых устройств. Подключите телефон по USB и разрешите отладку."
    fi
    ./gradlew "$task" --no-daemon "$@"
    adb install -r "$apk"
    success "Установлено на телефон!"
    adb shell monkey -p "$app_id" -c android.intent.category.LAUNCHER 1 &>/dev/null || true
    info "Приложение запущено"
}

# Сборка и копирование в dist/: $1 = задача Gradle, $2 = путь к APK, $3 = метка (prod-debug, dev-debug), остальное — доп. аргументы Gradle
build_debug() {
    local task="$1" apk="$2" label="$3"
    shift 3
    ./gradlew "$task" --no-daemon "$@"
    if [ -f "$apk" ]; then
        DEST="$OUTPUT_DIR/mase-${TIMESTAMP}-${label}.apk"
        cp "$apk" "$DEST"
        # Создать/обновить симлинк latest
        ln -sf "$DEST" "$OUTPUT_DIR/mase-latest-${label}.apk" 2>/dev/null || cp "$DEST" "$OUTPUT_DIR/mase-latest-${label}.apk"
        success "APK собран (${label})!"
        echo ""
        echo -e "  ${BOLD}Путь:${RESET}    $DEST"
        echo -e "  ${BOLD}Ярлык:${RESET}   $OUTPUT_DIR/mase-latest-${label}.apk"
        echo ""
        echo -e "${YELLOW}Установить на телефон:${RESET}"
        echo "  adb install -r \"$DEST\""
        echo ""
    else
        error "APK не найден: $apk"
    fi
}

if [ "$MODE" = "release" ]; then
    info "Сборка prod Release APK (неподписанный)..."
    ./gradlew :app:assembleProdRelease --no-daemon
    if [ -f "$APK_RELEASE" ]; then
        DEST="$OUTPUT_DIR/mase-${TIMESTAMP}-release-unsigned.apk"
        cp "$APK_RELEASE" "$DEST"
        success "Release APK: $DEST"
    else
        error "APK не найден: $APK_RELEASE"
    fi

elif [ "$MODE" = "install" ]; then
    info "Сборка и установка prod Debug APK на телефон..."
    install_on_phone :app:assembleProdDebug "$APK_DEBUG" com.mase.messenger

elif [ "$MODE" = "install-dev" ]; then
    info "Сборка и установка dev Debug APK на телефон..."
    install_on_phone :app:assembleDevDebug "$APK_DEV" com.mase.messenger.dev "${DEV_ARGS[@]}"

elif [ "$MODE" = "dev" ]; then
    info "Сборка dev Debug APK (локальный сервер)..."
    build_debug :app:assembleDevDebug "$APK_DEV" dev-debug "${DEV_ARGS[@]}"

else
    info "Сборка prod Debug APK..."
    build_debug :app:assembleProdDebug "$APK_DEBUG" prod-debug
fi
