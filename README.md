# Mase Messenger v0.2.0

Локальный мессенджер для Android с сервером на C++. Работает в Wi-Fi сети, без интернета и без облака.

## Архитектура

```
Android-клиент (Kotlin + Compose)
       │  TCP JSON (порт 5555)
       │  HTTP media (порт 5556)
       ▼
C++ сервер + SQLite
       │
       └─ avahi-publish → mDNS (_mase._tcp) — автообнаружение
```

**Автоподключение**: клиент ищет сервер через mDNS. Если не найден — использует кэш последнего адреса. Никакого ввода IP/порта.

---

## Что реализовано в v0.2.0

| Функция | Статус |
|---------|--------|
| Вход по номеру телефона + OTP | ✓ |
| Профиль (имя, username, bio) | ✓ |
| Список чатов с превью и временем | ✓ |
| Общий чат (для всех в сети) | ✓ |
| Личные чаты 1-на-1 | ✓ |
| Отправка текста | ✓ |
| Отправка фото | ✓ (HTTP upload → TCP event) |
| Просмотр фото в чате | ✓ |
| Список контактов / друзей | ✓ |
| Онлайн/оффлайн статус | ✓ |
| Статусы сообщений (✓ / ✓✓) | ✓ |
| Счётчик непрочитанных | ✓ |
| Приглашение по ссылке | ✓ (`mase://invite?username=...`) |
| Тёмная тема | ✓ |
| Авторы аватар (инициалы) | ✓ |
| SMS OTP | **dev-mock** (см. ниже) |
| E2E шифрование | нет (LAN MVP) |
| Push-уведомления | нет (LAN MVP) |

---

## Запуск сервера

### Зависимости (Debian/Ubuntu)
```bash
sudo apt install build-essential cmake libsqlite3-dev avahi-utils
```

### Сборка
```bash
cd cpp-server
cmake -S . -B build
cmake --build build -j$(nproc)
```

### Запуск
```bash
# Dev режим: код OTP приходит в приложение (без SMS)
export MASE_DEV_RETURN_OTP=1

./build/mase_server 5555 mase.sqlite
```

Сервер поднимает:
- TCP порт `5555` — основной протокол
- HTTP порт `5556` — загрузка/раздача фото

---

## Сборка APK

```bash
cd android-client

# Debug APK
./gradlew :app:assembleDebug

# APK: app/build/outputs/apk/debug/app-debug.apk
```

**Требования**: JDK 17, Android SDK API 34, файл `local.properties` с `sdk.dir=...`

---

## Dev-режим OTP (тест без SMS)

При `MASE_DEV_RETURN_OTP=1` сервер возвращает код прямо в JSON-ответе:
```json
{"type": "auth.otp_sent", "ttlSec": 600, "devCode": "123456"}
```
Приложение показывает его серым текстом под полем ввода кода.

В продакшене: убрать env-переменную, подключить SMS-провайдер (Twilio и т.д.) в `HandleAuthRequestOtp()` на сервере.

---

## Отправка фото

1. В чате нажать скрепку → выбрать фото из галереи
2. Клиент сжимает до 1280px JPEG и загружает на `http://host:5556/upload` (Bearer token)
3. Сервер сохраняет в `./media/`, возвращает `{"mediaId":"abc123.jpg"}`
4. Клиент отправляет TCP: `chat.photo.send` с `mediaId`
5. Сервер рассылает `evt.message` с `msgType:"photo"` и `mediaId`
6. Получатель загружает фото с `http://host:5556/media/abc123.jpg`

> Фото хранятся в `./media/` рядом с сервером. Папка создаётся автоматически.

---

## Invite-ссылки

Схема: `mase://invite?username=имя_пользователя`

- Пользователь делится ссылкой через share sheet
- При открытии ссылки: приложение ищет пользователя и предлагает добавить в контакты
- Для Android App Links (HTTPS) потребуется домен + `.well-known/assetlinks.json`

---

## Протокол (JSON over TCP)

### Без сессии
| Тип | Поля |
|-----|------|
| `auth.request_otp` | `phone` |
| `auth.verify_otp` | `phone`, `code` |
| `auth.resume` | `token` |

### С сессией (поле `token` обязательно)
| Тип | Описание |
|-----|----------|
| `profile.update` | `displayName`, `username`, `bio` |
| `friends.add` | `friendUserId` |
| `friends.list` | — |
| `user.lookup` | `username` или `userId` |
| `chat.global.send` | `body` |
| `chat.direct.send` | `peerUserId`, `body` |
| `chat.direct.open` | `peerUserId` |
| `chat.photo.send` | `peerUserId`, `mediaId` |
| `chat.history` | `chatId` |
| `chat.receipt` | `messageId` |
| `ping` | — |

### Сервер → клиент
| Тип | Описание |
|-----|----------|
| `auth.otp_sent` | `ttlSec`, опционально `devCode` |
| `auth.session` | `token`, `user` |
| `evt.message` | `message` с `id,chatId,senderId,otherUserId,body,ts,status,msgType,mediaId` |
| `evt.receipt` | `messageId`, `status` |
| `evt.presence` | `online[]` |
| `evt.profile` | `user` (обновление профиля) |
| `chat.open` | `chatId`, `peer` |
| `chat.history` | `chatId`, `messages[]` |
| `user.lookup` | `user` |
| `friends.list` | `friends[]` |
| `err` | `code` |

---

## Ручной тест-план

1. Телефон и ПК в одной Wi-Fi
2. Запустить сервер с `MASE_DEV_RETURN_OTP=1`
3. Установить APK на телефон
4. Ввести номер → получить код (отобразится в приложении) → войти
5. Заполнить профиль
6. Открыть вкладку «Контакты» → найти другого пользователя → написать
7. В чате: отправить текст, затем нажать скрепку → отправить фото
8. Убедиться что фото отображается у собеседника
9. Проверить счётчик непрочитанных в списке чатов
10. Поделиться invite-ссылкой из настроек

---

## Ограничения MVP

- Работает только в локальной сети (нет интернет-сервера)
- SMS — только mock через dev-флаг
- Нет E2E шифрования
- Нет push-уведомлений
- Медиа хранятся в `./media/` без ограничений по размеру — нет CDN
- mDNS требует avahi на сервере; без него — кэш последнего адреса
- Аватар пользователя — только инициалы (загрузка фото профиля не реализована)

---

## Структура проекта

```
mase/
├── cpp-server/
│   ├── CMakeLists.txt
│   └── src/main.cpp          # Сервер (TCP + HTTP media + SQLite)
├── android-client/
│   └── app/src/main/java/com/mase/messenger/
│       ├── data/local/        # Room DB (ChatEntity, MessageEntity)
│       ├── data/session/      # DataStore (SessionRepository)
│       ├── discovery/         # mDNS (ServerDiscovery)
│       ├── media/             # MediaUploader (HTTP фото)
│       ├── messaging/         # MessengerEngine (логика)
│       ├── network/           # TcpMessengerClient
│       └── ui/
│           ├── components/    # AvatarView, ConnectionBanner
│           ├── screens/       # Auth, ChatsList, Chat, Contacts, Settings
│           ├── theme/         # Цвета, тема (Telegram-like)
│           └── AppRoot.kt     # Навигационный шелл
└── README.md
```
