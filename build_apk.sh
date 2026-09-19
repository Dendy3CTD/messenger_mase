#!/usr/bin/env bash
# ============================================================
#  Mase Messenger — сборка APK
#  Использование:
#    ./build_apk.sh              — собрать debug APK
#    ./build_apk.sh release      — собрать release APK
#    ./build_apk.sh install      — собрать и установить на подключённый телефон
#    ./build_apk.sh server       — пересобрать только сервер (требует libsqlite3-dev)
# ============================================================
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ANDROID_DIR="$SCRIPT_DIR/android-client"
SERVER_DIR="$SCRIPT_DIR/cpp-server"
APK_DEBUG="$ANDROID_DIR/app/build/outputs/apk/debug/app-debug.apk"
APK_RELEASE="$ANDROID_DIR/app/build/outputs/apk/release/app-release-unsigned.apk"
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

# ── Сборка сервера ─────────────────────────────────────────
if [ "$MODE" = "server" ]; then
    info "Сборка C++ сервера..."
    cd "$SERVER_DIR"
    cmake -S . -B build -DCMAKE_BUILD_TYPE=Release
    cmake --build build -j"$(nproc)"
    success "Сервер собран: $SERVER_DIR/build/mase_server"
    echo ""
    echo -e "${YELLOW}Запуск сервера:${RESET}"
    echo "  export MASE_DEV_RETURN_OTP=1"
    echo "  cd $SERVER_DIR && ./build/mase_server 5555 mase.sqlite"
    exit 0
fi

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
cd "$ANDROID_DIR"
mkdir -p "$OUTPUT_DIR"

TIMESTAMP=$(date +"%Y%m%d_%H%M%S")

if [ "$MODE" = "release" ]; then
    info "Сборка Release APK (неподписанный)..."
    ./gradlew :app:assembleRelease --no-daemon
    if [ -f "$APK_RELEASE" ]; then
        DEST="$OUTPUT_DIR/mase-${TIMESTAMP}-release-unsigned.apk"
        cp "$APK_RELEASE" "$DEST"
        success "Release APK: $DEST"
    else
        error "APK не найден: $APK_RELEASE"
    fi

elif [ "$MODE" = "install" ]; then
    info "Сборка и установка Debug APK на телефон..."
    # Проверить adb
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
    ./gradlew :app:assembleDebug --no-daemon
    adb install -r "$APK_DEBUG"
    success "Установлено на телефон!"
    adb shell monkey -p com.mase.messenger -c android.intent.category.LAUNCHER 1 &>/dev/null || true
    info "Приложение запущено"

else
    info "Сборка Debug APK..."
    ./gradlew :app:assembleDebug --no-daemon
    if [ -f "$APK_DEBUG" ]; then
        DEST="$OUTPUT_DIR/mase-${TIMESTAMP}-debug.apk"
        cp "$APK_DEBUG" "$DEST"
        # Создать/обновить симлинк latest
        ln -sf "$DEST" "$OUTPUT_DIR/mase-latest-debug.apk" 2>/dev/null || cp "$DEST" "$OUTPUT_DIR/mase-latest-debug.apk"
        success "Debug APK собран!"
        echo ""
        echo -e "  ${BOLD}Путь:${RESET}    $DEST"
        echo -e "  ${BOLD}Ярлык:${RESET}   $OUTPUT_DIR/mase-latest-debug.apk"
        echo ""
        echo -e "${YELLOW}Установить на телефон:${RESET}"
        echo "  adb install -r \"$DEST\""
        echo ""
        echo -e "${YELLOW}Или запустить скрипт с install:${RESET}"
        echo "  ./build_apk.sh install"
    else
        error "APK не найден: $APK_DEBUG"
    fi
fi
