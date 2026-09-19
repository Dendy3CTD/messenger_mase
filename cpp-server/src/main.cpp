// Mase server: TCP + newline-delimited JSON, SQLite persistence, optional mDNS via avahi-publish.
#include <arpa/inet.h>
#include <netinet/in.h>
#include <sqlite3.h>
#include <sys/socket.h>
#include <sys/wait.h>
#include <unistd.h>

#include <atomic>
#include <array>
#include <cctype>
#include <cerrno>
#include <chrono>
#include <csignal>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <iomanip>
#include <sys/stat.h>
#include <iostream>
#include <mutex>
#include <optional>
#include <random>
#include <sstream>
#include <string>
#include <string_view>
#include <thread>
#include <unordered_map>
#include <vector>

#ifndef MSG_NOSIGNAL
#define MSG_NOSIGNAL 0
#endif

namespace {

constexpr int kDefaultPort = 5555;
constexpr int kBacklog = 32;
constexpr size_t kBufferSize = 65536;
constexpr int64_t kGlobalChatId = 1;
const std::string kMediaDir = "./media/";

std::atomic<bool> g_running{true};
sqlite3* g_db = nullptr;
std::mutex g_db_mutex;

std::mutex g_net_mutex;
std::unordered_map<int, int64_t> g_fd_user;           // authenticated TCP fd -> user id
std::unordered_map<int64_t, int> g_user_fd;          // user id -> single active fd
std::unordered_map<int64_t, std::string> g_user_token; // user id -> session token (last)

// Typing state: userId -> chatId (which chat they are currently typing in)
std::mutex g_typing_mutex;
std::unordered_map<int64_t, int64_t> g_typing;

void HandleSignal(int) {
    g_running = false;
}

// ── SHA-256 (RFC 6234, no external deps) ────────────────────────────────────
std::string Sha256Hex(const std::string& input) {
    auto rotr = [](uint32_t x, uint32_t n) { return (x >> n) | (x << (32 - n)); };
    uint32_t h[8] = {
        0x6a09e667u, 0xbb67ae85u, 0x3c6ef372u, 0xa54ff53au,
        0x510e527fu, 0x9b05688cu, 0x1f83d9abu, 0x5be0cd19u
    };
    static const uint32_t k[64] = {
        0x428a2f98u,0x71374491u,0xb5c0fbcfu,0xe9b5dba5u,0x3956c25bu,0x59f111f1u,0x923f82a4u,0xab1c5ed5u,
        0xd807aa98u,0x12835b01u,0x243185beu,0x550c7dc3u,0x72be5d74u,0x80deb1feu,0x9bdc06a7u,0xc19bf174u,
        0xe49b69c1u,0xefbe4786u,0x0fc19dc6u,0x240ca1ccu,0x2de92c6fu,0x4a7484aau,0x5cb0a9dcu,0x76f988dau,
        0x983e5152u,0xa831c66du,0xb00327c8u,0xbf597fc7u,0xc6e00bf3u,0xd5a79147u,0x06ca6351u,0x14292967u,
        0x27b70a85u,0x2e1b2138u,0x4d2c6dfcu,0x53380d13u,0x650a7354u,0x766a0abbu,0x81c2c92eu,0x92722c85u,
        0xa2bfe8a1u,0xa81a664bu,0xc24b8b70u,0xc76c51a3u,0xd192e819u,0xd6990624u,0xf40e3585u,0x106aa070u,
        0x19a4c116u,0x1e376c08u,0x2748774cu,0x34b0bcb5u,0x391c0cb3u,0x4ed8aa4au,0x5b9cca4fu,0x682e6ff3u,
        0x748f82eeu,0x78a5636fu,0x84c87814u,0x8cc70208u,0x90befffau,0xa4506cebu,0xbef9a3f7u,0xc67178f2u
    };
    // Pre-process: add padding
    std::vector<uint8_t> msg(input.begin(), input.end());
    uint64_t bit_len = static_cast<uint64_t>(input.size()) * 8;
    msg.push_back(0x80);
    while (msg.size() % 64 != 56) msg.push_back(0);
    for (int i = 7; i >= 0; --i) msg.push_back(static_cast<uint8_t>((bit_len >> (i * 8)) & 0xff));
    // Process blocks
    for (size_t i = 0; i < msg.size(); i += 64) {
        uint32_t w[64] = {};
        for (int j = 0; j < 16; ++j)
            w[j] = (uint32_t(msg[i+j*4])<<24)|(uint32_t(msg[i+j*4+1])<<16)|
                   (uint32_t(msg[i+j*4+2])<<8)|uint32_t(msg[i+j*4+3]);
        for (int j = 16; j < 64; ++j) {
            uint32_t s0 = rotr(w[j-15],7)^rotr(w[j-15],18)^(w[j-15]>>3);
            uint32_t s1 = rotr(w[j-2],17)^rotr(w[j-2],19)^(w[j-2]>>10);
            w[j] = w[j-16]+s0+w[j-7]+s1;
        }
        uint32_t a=h[0],b=h[1],c=h[2],d=h[3],e=h[4],f=h[5],g=h[6],hh=h[7];
        for (int j = 0; j < 64; ++j) {
            uint32_t S1=rotr(e,6)^rotr(e,11)^rotr(e,25);
            uint32_t ch=(e&f)^(~e&g);
            uint32_t tmp1=hh+S1+ch+k[j]+w[j];
            uint32_t S0=rotr(a,2)^rotr(a,13)^rotr(a,22);
            uint32_t maj=(a&b)^(a&c)^(b&c);
            uint32_t tmp2=S0+maj;
            hh=g; g=f; f=e; e=d+tmp1;
            d=c; c=b; b=a; a=tmp1+tmp2;
        }
        h[0]+=a;h[1]+=b;h[2]+=c;h[3]+=d;
        h[4]+=e;h[5]+=f;h[6]+=g;h[7]+=hh;
    }
    std::ostringstream oss;
    oss << std::hex << std::setfill('0');
    for (uint32_t v : h) oss << std::setw(8) << v;
    return oss.str();
}

std::string JsonEscape(std::string_view s) {
    std::string o;
    o.reserve(s.size() + 8);
    for (char c : s) {
        switch (c) {
            case '"':
                o += "\\\"";
                break;
            case '\\':
                o += "\\\\";
                break;
            case '\n':
                o += "\\n";
                break;
            case '\r':
                o += "\\r";
                break;
            case '\t':
                o += "\\t";
                break;
            default:
                o += c;
        }
    }
    return o;
}

std::optional<std::string> JsonGetString(const std::string& j, std::string_view key) {
    const std::string needle = std::string("\"") + std::string(key) + "\"";
    size_t p = j.find(needle);
    if (p == std::string::npos) {
        return std::nullopt;
    }
    p = j.find(':', p);
    if (p == std::string::npos) {
        return std::nullopt;
    }
    while (p < j.size() && (j[p] == ':' || j[p] == ' ' || j[p] == '\t')) {
        ++p;
    }
    if (p >= j.size()) {
        return std::nullopt;
    }
    if (j[p] == '"') {
        size_t start = p + 1;
        size_t i = start;
        std::string out;
        while (i < j.size()) {
            if (j[i] == '\\' && i + 1 < j.size()) {
                const char n = j[i + 1];
                if (n == 'n') {
                    out += '\n';
                } else if (n == 'r') {
                    out += '\r';
                } else if (n == 't') {
                    out += '\t';
                } else if (n == '"' || n == '\\') {
                    out += n;
                } else {
                    out += n;
                }
                i += 2;
                continue;
            }
            if (j[i] == '"') {
                break;
            }
            out += j[i];
            ++i;
        }
        return out;
    }
    // number or bare token
    size_t start = p;
    while (start < j.size() && (j[start] == ' ' || j[start] == '\t')) {
        ++start;
    }
    size_t end = start;
    while (end < j.size() && std::string("0123456789").find(j[end]) != std::string::npos) {
        ++end;
    }
    if (end == start) {
        return std::nullopt;
    }
    return j.substr(start, end - start);
}

std::optional<int64_t> JsonGetInt(const std::string& j, std::string_view key) {
    const auto s = JsonGetString(j, key);
    if (!s) {
        return std::nullopt;
    }
    try {
        return std::stoll(*s);
    } catch (...) {
        return std::nullopt;
    }
}

bool DbExec(const char* sql) {
    char* err = nullptr;
    const int rc = sqlite3_exec(g_db, sql, nullptr, nullptr, &err);
    if (rc != SQLITE_OK) {
        std::cerr << "SQL error: " << (err ? err : "?") << "\n";
        sqlite3_free(err);
        return false;
    }
    return true;
}

bool DbInit(const std::string& path) {
    if (sqlite3_open(path.c_str(), &g_db) != SQLITE_OK) {
        std::cerr << "sqlite3_open failed\n";
        return false;
    }
    sqlite3_busy_timeout(g_db, 3000);
    const char* schema = R"SQL(
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  phone TEXT NOT NULL UNIQUE,
  username TEXT NOT NULL UNIQUE,
  display_name TEXT NOT NULL DEFAULT '',
  bio TEXT NOT NULL DEFAULT '',
  avatar_b64 TEXT,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS otp_codes (
  phone TEXT PRIMARY KEY,
  code TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
  token TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS chats (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL,
  u1 INTEGER,
  u2 INTEGER
);
CREATE TABLE IF NOT EXISTS messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  chat_id INTEGER NOT NULL,
  sender_id INTEGER NOT NULL,
  body TEXT NOT NULL,
  ts INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'sent',
  msg_type TEXT NOT NULL DEFAULT 'text',
  media_url TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS friendships (
  user_id INTEGER NOT NULL,
  friend_id INTEGER NOT NULL,
  PRIMARY KEY (user_id, friend_id)
);
CREATE INDEX IF NOT EXISTS idx_messages_chat_ts ON messages(chat_id, ts);
)SQL";
    if (!DbExec(schema)) {
        return false;
    }
    // Migrate existing databases (silently ignored if columns already exist)
    sqlite3_exec(g_db, "ALTER TABLE messages ADD COLUMN msg_type TEXT NOT NULL DEFAULT 'text'", nullptr, nullptr, nullptr);
    sqlite3_exec(g_db, "ALTER TABLE messages ADD COLUMN media_url TEXT NOT NULL DEFAULT ''", nullptr, nullptr, nullptr);
    sqlite3_exec(g_db, "ALTER TABLE users ADD COLUMN avatar_media_id TEXT NOT NULL DEFAULT ''", nullptr, nullptr, nullptr);
    sqlite3_exec(g_db, "ALTER TABLE users ADD COLUMN password_hash TEXT NOT NULL DEFAULT ''", nullptr, nullptr, nullptr);
    // Global chat row
    {
        std::lock_guard<std::mutex> lock(g_db_mutex);
        sqlite3_stmt* st = nullptr;
        const char* ins =
            "INSERT OR IGNORE INTO chats (id, kind, u1, u2) VALUES (1, 'global', NULL, NULL)";
        sqlite3_prepare_v2(g_db, ins, -1, &st, nullptr);
        sqlite3_step(st);
        sqlite3_finalize(st);
    }
    return true;
}

std::string RandomHex(size_t bytes) {
    static thread_local std::mt19937_64 gen{std::random_device{}()};
    std::uniform_int_distribution<uint64_t> dist;
    std::ostringstream oss;
    oss << std::hex;
    for (size_t i = 0; i < bytes; ++i) {
        oss << std::setw(2) << std::setfill('0') << (dist(gen) & 0xFF);
    }
    return oss.str();
}

std::string RandomDigits(size_t n) {
    static thread_local std::mt19937 gen{std::random_device{}()};
    std::uniform_int_distribution<int> dist(0, 9);
    std::string s;
    s.reserve(n);
    for (size_t i = 0; i < n; ++i) {
        s.push_back(char('0' + dist(gen)));
    }
    return s;
}

bool SendLine(int fd, const std::string& line) {
    const auto sent = send(fd, line.data(), line.size(), MSG_NOSIGNAL);
    return static_cast<size_t>(sent) == line.size();
}

void UnregisterClient(int fd) {
    int64_t uid = 0;
    {
        std::lock_guard<std::mutex> lock(g_net_mutex);
        auto it = g_fd_user.find(fd);
        if (it == g_fd_user.end()) {
            return;
        }
        uid = it->second;
        g_fd_user.erase(it);
        auto jt = g_user_fd.find(uid);
        if (jt != g_user_fd.end() && jt->second == fd) {
            g_user_fd.erase(jt);
        }
    }
    if (uid > 0) {
        std::lock_guard<std::mutex> lock(g_typing_mutex);
        g_typing.erase(uid);
    }
}

void RegisterSessionForFd(int fd, int64_t user_id, std::string token) {
    std::lock_guard<std::mutex> lock(g_net_mutex);
    g_fd_user[fd] = user_id;
    g_user_fd[user_id] = fd;
    g_user_token[user_id] = std::move(token);
}

std::optional<int64_t> ValidateToken(const std::string& token) {
    if (token.empty()) {
        return std::nullopt;
    }
    std::lock_guard<std::mutex> lock(g_db_mutex);
    sqlite3_stmt* st = nullptr;
    const char* q = "SELECT user_id, expires_at FROM sessions WHERE token = ?";
    if (sqlite3_prepare_v2(g_db, q, -1, &st, nullptr) != SQLITE_OK) {
        return std::nullopt;
    }
    sqlite3_bind_text(st, 1, token.c_str(), -1, SQLITE_TRANSIENT);
    std::optional<int64_t> uid;
    if (sqlite3_step(st) == SQLITE_ROW) {
        const int64_t u = sqlite3_column_int64(st, 0);
        const int64_t exp = sqlite3_column_int64(st, 1);
        const int64_t now = std::chrono::duration_cast<std::chrono::seconds>(
                                std::chrono::system_clock::now().time_since_epoch())
                                .count();
        if (now <= exp) {
            uid = u;
        }
    }
    sqlite3_finalize(st);
    return uid;
}

std::optional<int64_t> AuthUidForLine(const std::string& line, int fd) {
    const auto tok = JsonGetString(line, "token");
    if (!tok || tok->empty()) {
        return std::nullopt;
    }
    const auto uid = ValidateToken(*tok);
    if (!uid) {
        return std::nullopt;
    }
    std::lock_guard<std::mutex> lock(g_net_mutex);
    const auto it = g_fd_user.find(fd);
    if (it == g_fd_user.end() || it->second != *uid) {
        return std::nullopt;
    }
    return uid;
}

void BroadcastAuthenticated(const std::string& line, int except_fd = -1) {
    std::vector<int> fds;
    {
        std::lock_guard<std::mutex> lock(g_net_mutex);
        for (const auto& [f, uid] : g_fd_user) {
            (void)uid;
            if (f != except_fd) {
                fds.push_back(f);
            }
        }
    }
    for (int f : fds) {
        if (!SendLine(f, line)) {
            std::cerr << "broadcast send fail fd=" << f << "\n";
        }
    }
}

void SendToUser(int64_t user_id, const std::string& line) {
    int fd = -1;
    {
        std::lock_guard<std::mutex> lock(g_net_mutex);
        auto it = g_user_fd.find(user_id);
        if (it != g_user_fd.end()) {
            fd = it->second;
        }
    }
    if (fd >= 0) {
        SendLine(fd, line);
    }
}

std::string UserJsonById(int64_t id) {
    std::lock_guard<std::mutex> lock(g_db_mutex);
    sqlite3_stmt* st = nullptr;
    const char* q =
        "SELECT id, phone, username, display_name, bio, avatar_media_id FROM users WHERE id = ?";
    sqlite3_prepare_v2(g_db, q, -1, &st, nullptr);
    sqlite3_bind_int64(st, 1, id);
    if (sqlite3_step(st) != SQLITE_ROW) {
        sqlite3_finalize(st);
        return "{}";
    }
    const int64_t uid = sqlite3_column_int64(st, 0);
    // Копируем строки ДО finalize — после finalize указатели становятся недействительными
    const auto col_str = [&](int col) -> std::string {
        const char* p = reinterpret_cast<const char*>(sqlite3_column_text(st, col));
        return p ? std::string(p) : "";
    };
    const std::string phone      = col_str(1);
    const std::string username   = col_str(2);
    const std::string dn         = col_str(3);
    const std::string bio        = col_str(4);
    const std::string avatar_mid = col_str(5);
    sqlite3_finalize(st);
    std::ostringstream oss;
    oss << "{\"id\":" << uid << ",\"phone\":\"" << JsonEscape(phone) << "\","
        << "\"username\":\"" << JsonEscape(username) << "\","
        << "\"displayName\":\"" << JsonEscape(dn) << "\","
        << "\"bio\":\"" << JsonEscape(bio) << "\","
        << "\"avatarMediaId\":\"" << JsonEscape(avatar_mid) << "\"}";
    return oss.str();
}

void PushPresenceSnapshot() {
    std::string payload;
    {
        std::lock_guard<std::mutex> lk(g_net_mutex);
        std::ostringstream oss;
        oss << "{\"type\":\"evt.presence\",\"online\":[";
        bool first = true;
        for (const auto& [uid, fd] : g_user_fd) {
            (void)fd;
            if (!first) {
                oss << ',';
            }
            first = false;
            oss << UserJsonById(uid);
        }
        oss << "]}\n";
        payload = oss.str();
    }
    BroadcastAuthenticated(payload, -1);
}

bool UsernameTaken(const std::string& un, int64_t except_id) {
    std::lock_guard<std::mutex> lock(g_db_mutex);
    sqlite3_stmt* st = nullptr;
    sqlite3_prepare_v2(g_db, "SELECT id FROM users WHERE username = ?", -1, &st, nullptr);
    sqlite3_bind_text(st, 1, un.c_str(), -1, SQLITE_TRANSIENT);
    bool taken = false;
    if (sqlite3_step(st) == SQLITE_ROW) {
        const int64_t id = sqlite3_column_int64(st, 0);
        taken = (id != except_id);
    }
    sqlite3_finalize(st);
    return taken;
}

std::optional<int64_t> FindDirectChat(int64_t a, int64_t b) {
    const int64_t lo = std::min(a, b);
    const int64_t hi = std::max(a, b);
    std::lock_guard<std::mutex> lock(g_db_mutex);
    sqlite3_stmt* st = nullptr;
    sqlite3_prepare_v2(
        g_db, "SELECT id FROM chats WHERE kind='direct' AND u1=? AND u2=?", -1, &st, nullptr);
    sqlite3_bind_int64(st, 1, lo);
    sqlite3_bind_int64(st, 2, hi);
    std::optional<int64_t> cid;
    if (sqlite3_step(st) == SQLITE_ROW) {
        cid = sqlite3_column_int64(st, 0);
    }
    sqlite3_finalize(st);
    return cid;
}

int64_t CreateDirectChat(int64_t a, int64_t b) {
    const int64_t lo = std::min(a, b);
    const int64_t hi = std::max(a, b);
    std::lock_guard<std::mutex> lock(g_db_mutex);
    sqlite3_stmt* st = nullptr;
    sqlite3_prepare_v2(
        g_db, "INSERT INTO chats (kind, u1, u2) VALUES ('direct', ?, ?)", -1, &st, nullptr);
    sqlite3_bind_int64(st, 1, lo);
    sqlite3_bind_int64(st, 2, hi);
    sqlite3_step(st);
    sqlite3_finalize(st);
    return sqlite3_last_insert_rowid(g_db);
}

int64_t EnsureDirectChat(int64_t a, int64_t b) {
    auto ex = FindDirectChat(a, b);
    if (ex) {
        return *ex;
    }
    return CreateDirectChat(a, b);
}

// Общий хелпер: создаём сессию и отвечаем auth.session
void IssueSession(int fd, int64_t uid) {
    const std::string token = RandomHex(16);
    const int64_t exp_sess =
        std::chrono::duration_cast<std::chrono::seconds>(
            std::chrono::system_clock::now().time_since_epoch())
            .count() + 60L * 60L * 24L * 30L;
    {
        std::lock_guard<std::mutex> lock(g_db_mutex);
        sqlite3_stmt* st = nullptr;
        sqlite3_prepare_v2(
            g_db, "INSERT INTO sessions(token, user_id, expires_at) VALUES(?,?,?)", -1, &st, nullptr);
        sqlite3_bind_text(st, 1, token.c_str(), -1, SQLITE_TRANSIENT);
        sqlite3_bind_int64(st, 2, uid);
        sqlite3_bind_int64(st, 3, exp_sess);
        sqlite3_step(st);
        sqlite3_finalize(st);
    }
    UnregisterClient(fd);
    RegisterSessionForFd(fd, uid, token);
    std::ostringstream oss;
    oss << "{\"type\":\"auth.session\",\"token\":\"" << token << "\",\"user\":"
        << UserJsonById(uid) << "}\n";
    SendLine(fd, oss.str());
    PushPresenceSnapshot();
}

void HandleAuthLogin(int fd, const std::string& line) {
    const auto phone = JsonGetString(line, "phone");
    const auto password = JsonGetString(line, "password");
    if (!phone || phone->size() < 4 || !password || password->empty()) {
        SendLine(fd, "{\"type\":\"err\",\"code\":\"bad_request\"}\n");
        return;
    }
    const std::string hash = Sha256Hex(*password);
    int64_t uid = 0;
    {
        std::lock_guard<std::mutex> lock(g_db_mutex);
        sqlite3_stmt* st = nullptr;
        sqlite3_prepare_v2(g_db,
            "SELECT id, password_hash FROM users WHERE phone=?", -1, &st, nullptr);
        sqlite3_bind_text(st, 1, phone->c_str(), -1, SQLITE_TRANSIENT);
        if (sqlite3_step(st) != SQLITE_ROW) {
            sqlite3_finalize(st);
            std::cout << "[AUTH] LOGIN FAIL phone=" << *phone << " reason=not_found\n";
            SendLine(fd, "{\"type\":\"err\",\"code\":\"bad_credentials\"}\n");
            return;
        }
        uid = sqlite3_column_int64(st, 0);
        const char* stored = reinterpret_cast<const char*>(sqlite3_column_text(st, 1));
        const std::string stored_hash = stored ? stored : "";
        sqlite3_finalize(st);
        if (stored_hash != hash) {
            std::cout << "[AUTH] LOGIN FAIL phone=" << *phone << " reason=wrong_password\n";
            SendLine(fd, "{\"type\":\"err\",\"code\":\"bad_credentials\"}\n");
            return;
        }
    }
    std::cout << "[AUTH] LOGIN OK phone=" << *phone << " uid=" << uid << "\n";
    IssueSession(fd, uid);
}

void HandleAuthRegister(int fd, const std::string& line) {
    const auto phone = JsonGetString(line, "phone");
    const auto password = JsonGetString(line, "password");
    const auto display_name = JsonGetString(line, "displayName");
    const auto username_raw = JsonGetString(line, "username");
    if (!phone || phone->size() < 4 || !password || password->size() < 4) {
        SendLine(fd, "{\"type\":\"err\",\"code\":\"bad_request\"}\n");
        return;
    }
    const std::string hash = Sha256Hex(*password);
    // Нормализуем username
    std::string username;
    if (username_raw && !username_raw->empty()) {
        username = *username_raw;
        for (char& c : username) c = char(std::tolower(static_cast<unsigned char>(c)));
    } else {
        username = std::string("u") + RandomDigits(7);
    }
    const std::string dn = (display_name && !display_name->empty()) ? *display_name : username;
    int64_t uid = 0;
    {
        std::lock_guard<std::mutex> lock(g_db_mutex);
        // Проверяем телефон
        sqlite3_stmt* chk = nullptr;
        sqlite3_prepare_v2(g_db, "SELECT id FROM users WHERE phone=?", -1, &chk, nullptr);
        sqlite3_bind_text(chk, 1, phone->c_str(), -1, SQLITE_TRANSIENT);
        if (sqlite3_step(chk) == SQLITE_ROW) {
            sqlite3_finalize(chk);
            std::cout << "[AUTH] REGISTER FAIL phone=" << *phone << " reason=phone_taken\n";
            SendLine(fd, "{\"type\":\"err\",\"code\":\"phone_taken\"}\n");
            return;
        }
        sqlite3_finalize(chk);
        // Проверяем username
        sqlite3_stmt* uchk = nullptr;
        sqlite3_prepare_v2(g_db, "SELECT id FROM users WHERE username=?", -1, &uchk, nullptr);
        sqlite3_bind_text(uchk, 1, username.c_str(), -1, SQLITE_TRANSIENT);
        if (sqlite3_step(uchk) == SQLITE_ROW) {
            sqlite3_finalize(uchk);
            std::cout << "[AUTH] REGISTER FAIL username=" << username << " reason=username_taken\n";
            SendLine(fd, "{\"type\":\"err\",\"code\":\"username_taken\"}\n");
            return;
        }
        sqlite3_finalize(uchk);
        const int64_t now = std::chrono::duration_cast<std::chrono::seconds>(
                                std::chrono::system_clock::now().time_since_epoch()).count();
        sqlite3_stmt* ins = nullptr;
        sqlite3_prepare_v2(g_db,
            "INSERT INTO users(phone, username, display_name, bio, password_hash, created_at) "
            "VALUES(?,?,?,?,?,?)", -1, &ins, nullptr);
        sqlite3_bind_text(ins, 1, phone->c_str(), -1, SQLITE_TRANSIENT);
        sqlite3_bind_text(ins, 2, username.c_str(), -1, SQLITE_TRANSIENT);
        sqlite3_bind_text(ins, 3, dn.c_str(), -1, SQLITE_TRANSIENT);
        sqlite3_bind_text(ins, 4, "", -1, SQLITE_TRANSIENT);
        sqlite3_bind_text(ins, 5, hash.c_str(), -1, SQLITE_TRANSIENT);
        sqlite3_bind_int64(ins, 6, now);
        sqlite3_step(ins);
        sqlite3_finalize(ins);
        uid = sqlite3_last_insert_rowid(g_db);
    }
    std::cout << "[AUTH] REGISTER OK phone=" << *phone << " username=" << username << " uid=" << uid << "\n";
    IssueSession(fd, uid);
}

void HandleProfileUpdate(int fd, const std::string& line, int64_t uid) {
    const auto dn = JsonGetString(line, "displayName");
    const auto un = JsonGetString(line, "username");
    const auto bio = JsonGetString(line, "bio");
    const auto avatar = JsonGetString(line, "avatarBase64");
    if (un && !un->empty()) {
        std::string lower = *un;
        for (char& c : lower) {
            c = char(std::tolower(static_cast<unsigned char>(c)));
        }
        if (UsernameTaken(lower, uid)) {
            SendLine(fd, "{\"type\":\"err\",\"code\":\"username_taken\"}\n");
            return;
        }
    }
    {
        std::lock_guard<std::mutex> lock(g_db_mutex);
        sqlite3_stmt* st = nullptr;
        std::string sql = "UPDATE users SET ";
        bool first = true;
        if (dn) {
            if (!first) {
                sql += ",";
            }
            first = false;
            sql += "display_name=?";
        }
        if (un) {
            if (!first) {
                sql += ",";
            }
            first = false;
            sql += "username=?";
        }
        if (bio) {
            if (!first) {
                sql += ",";
            }
            first = false;
            sql += "bio=?";
        }
        if (avatar) {
            if (!first) {
                sql += ",";
            }
            first = false;
            sql += "avatar_b64=?";
        }
        if (first) {
            return;
        }
        sql += " WHERE id=?";
        sqlite3_prepare_v2(g_db, sql.c_str(), -1, &st, nullptr);
        int bi = 1;
        if (dn) {
            sqlite3_bind_text(st, bi++, dn->c_str(), -1, SQLITE_TRANSIENT);
        }
        if (un) {
            std::string lower = *un;
            for (char& c : lower) {
                c = char(std::tolower(static_cast<unsigned char>(c)));
            }
            sqlite3_bind_text(st, bi++, lower.c_str(), -1, SQLITE_TRANSIENT);
        }
        if (bio) {
            sqlite3_bind_text(st, bi++, bio->c_str(), -1, SQLITE_TRANSIENT);
        }
        if (avatar) {
            sqlite3_bind_text(st, bi++, avatar->c_str(), -1, SQLITE_TRANSIENT);
        }
        sqlite3_bind_int64(st, bi, uid);
        sqlite3_step(st);
        sqlite3_finalize(st);
    }
    SendLine(fd, std::string("{\"type\":\"profile.updated\",\"user\":") + UserJsonById(uid) + "}\n");
    BroadcastAuthenticated(
        std::string("{\"type\":\"evt.profile\",\"user\":") + UserJsonById(uid) + "}\n", fd);
}

void HandleFriendsAdd(int fd, const std::string& line, int64_t uid) {
    const auto peer = JsonGetInt(line, "friendUserId");
    if (!peer || *peer == uid) {
        SendLine(fd, "{\"type\":\"err\",\"code\":\"bad_friend\"}\n");
        return;
    }
    {
        std::lock_guard<std::mutex> lock(g_db_mutex);
        sqlite3_stmt* chk = nullptr;
        sqlite3_prepare_v2(g_db, "SELECT id FROM users WHERE id=?", -1, &chk, nullptr);
        sqlite3_bind_int64(chk, 1, *peer);
        if (sqlite3_step(chk) != SQLITE_ROW) {
            sqlite3_finalize(chk);
            SendLine(fd, "{\"type\":\"err\",\"code\":\"no_user\"}\n");
            return;
        }
        sqlite3_finalize(chk);
        sqlite3_stmt* ins = nullptr;
        sqlite3_prepare_v2(g_db, "INSERT OR IGNORE INTO friendships(user_id, friend_id) VALUES(?,?)",
                           -1, &ins, nullptr);
        sqlite3_bind_int64(ins, 1, uid);
        sqlite3_bind_int64(ins, 2, *peer);
        sqlite3_step(ins);
        sqlite3_finalize(ins);
        sqlite3_prepare_v2(g_db, "INSERT OR IGNORE INTO friendships(user_id, friend_id) VALUES(?,?)",
                           -1, &ins, nullptr);
        sqlite3_bind_int64(ins, 1, *peer);
        sqlite3_bind_int64(ins, 2, uid);
        sqlite3_step(ins);
        sqlite3_finalize(ins);
    }
    SendLine(fd, "{\"type\":\"friends.ok\"}\n");
}

void SendFriendsList(int fd, int64_t uid) {
    std::lock_guard<std::mutex> lock(g_db_mutex);
    sqlite3_stmt* st = nullptr;
    sqlite3_prepare_v2(
        g_db,
        "SELECT u.id, u.phone, u.username, u.display_name, u.bio FROM users u "
        "JOIN friendships f ON f.friend_id = u.id WHERE f.user_id=?",
        -1, &st, nullptr);
    sqlite3_bind_int64(st, 1, uid);
    std::ostringstream oss;
    oss << "{\"type\":\"friends.list\",\"friends\":[";
    bool first = true;
    while (sqlite3_step(st) == SQLITE_ROW) {
        if (!first) {
            oss << ',';
        }
        first = false;
        const int64_t id = sqlite3_column_int64(st, 0);
        const char* phone = reinterpret_cast<const char*>(sqlite3_column_text(st, 1));
        const char* username = reinterpret_cast<const char*>(sqlite3_column_text(st, 2));
        const char* dn = reinterpret_cast<const char*>(sqlite3_column_text(st, 3));
        const char* bio = reinterpret_cast<const char*>(sqlite3_column_text(st, 4));
        oss << "{\"id\":" << id << ",\"phone\":\"" << JsonEscape(phone ? phone : "") << "\","
            << "\"username\":\"" << JsonEscape(username ? username : "") << "\","
            << "\"displayName\":\"" << JsonEscape(dn ? dn : "") << "\","
            << "\"bio\":\"" << JsonEscape(bio ? bio : "") << "\"}";
    }
    sqlite3_finalize(st);
    oss << "]}\n";
    SendLine(fd, oss.str());
}

void HandleChatGlobal(int fd, const std::string& line, int64_t uid) {
    const auto body = JsonGetString(line, "body");
    if (!body || body->empty()) {
        return;
    }
    const int64_t ts = std::chrono::duration_cast<std::chrono::milliseconds>(
                           std::chrono::system_clock::now().time_since_epoch())
                           .count();
    int64_t mid = 0;
    {
        std::lock_guard<std::mutex> lock(g_db_mutex);
        sqlite3_stmt* st = nullptr;
        sqlite3_prepare_v2(
            g_db,
            "INSERT INTO messages(chat_id, sender_id, body, ts, status) VALUES(?,?,?,?,?)", -1, &st,
            nullptr);
        sqlite3_bind_int64(st, 1, kGlobalChatId);
        sqlite3_bind_int64(st, 2, uid);
        sqlite3_bind_text(st, 3, body->c_str(), -1, SQLITE_TRANSIENT);
        sqlite3_bind_int64(st, 4, ts);
        sqlite3_bind_text(st, 5, "sent", -1, SQLITE_TRANSIENT);
        sqlite3_step(st);
        sqlite3_finalize(st);
        mid = sqlite3_last_insert_rowid(g_db);
    }
    std::ostringstream evt;
    evt << "{\"type\":\"evt.message\",\"message\":{\"id\":" << mid << ",\"chatId\":" << kGlobalChatId
        << ",\"senderId\":" << uid << ",\"body\":\"" << JsonEscape(*body) << "\",\"ts\":" << ts
        << ",\"status\":\"sent\"}}\n";
    BroadcastAuthenticated(evt.str(), -1);
}

void HandleChatDirect(int fd, const std::string& line, int64_t uid) {
    const auto body = JsonGetString(line, "body");
    const auto peer = JsonGetInt(line, "peerUserId");
    if (!body || body->empty() || !peer) {
        SendLine(fd, "{\"type\":\"err\",\"code\":\"bad_dm\"}\n");
        return;
    }
    const int64_t chat_id = EnsureDirectChat(uid, *peer);
    const int64_t ts = std::chrono::duration_cast<std::chrono::milliseconds>(
                           std::chrono::system_clock::now().time_since_epoch())
                           .count();
    int64_t mid = 0;
    {
        std::lock_guard<std::mutex> lock(g_db_mutex);
        sqlite3_stmt* st = nullptr;
        sqlite3_prepare_v2(
            g_db,
            "INSERT INTO messages(chat_id, sender_id, body, ts, status) VALUES(?,?,?,?,?)", -1, &st,
            nullptr);
        sqlite3_bind_int64(st, 1, chat_id);
        sqlite3_bind_int64(st, 2, uid);
        sqlite3_bind_text(st, 3, body->c_str(), -1, SQLITE_TRANSIENT);
        sqlite3_bind_int64(st, 4, ts);
        sqlite3_bind_text(st, 5, "sent", -1, SQLITE_TRANSIENT);
        sqlite3_step(st);
        sqlite3_finalize(st);
        mid = sqlite3_last_insert_rowid(g_db);
    }
    auto build_evt = [&](int64_t other_uid) {
        std::ostringstream e;
        e << "{\"type\":\"evt.message\",\"message\":{\"id\":" << mid << ",\"chatId\":" << chat_id
          << ",\"senderId\":" << uid << ",\"otherUserId\":" << other_uid << ",\"body\":\""
          << JsonEscape(*body) << "\",\"ts\":" << ts << ",\"status\":\"sent\"}}\n";
        return e.str();
    };
    SendToUser(*peer, build_evt(uid));
    SendLine(fd, build_evt(*peer));
}

void HandleHistory(int fd, const std::string& line, int64_t uid) {
    const auto chat = JsonGetInt(line, "chatId");
    if (!chat) {
        return;
    }
    if (*chat != kGlobalChatId) {
        std::lock_guard<std::mutex> lock(g_db_mutex);
        sqlite3_stmt* st = nullptr;
        sqlite3_prepare_v2(
            g_db, "SELECT u1, u2 FROM chats WHERE id=? AND kind='direct'", -1, &st, nullptr);
        sqlite3_bind_int64(st, 1, *chat);
        if (sqlite3_step(st) != SQLITE_ROW) {
            sqlite3_finalize(st);
            SendLine(fd, "{\"type\":\"err\",\"code\":\"no_chat\"}\n");
            return;
        }
        const int64_t u1 = sqlite3_column_int64(st, 0);
        const int64_t u2 = sqlite3_column_int64(st, 1);
        sqlite3_finalize(st);
        if (uid != u1 && uid != u2) {
            SendLine(fd, "{\"type\":\"err\",\"code\":\"forbidden\"}\n");
            return;
        }
    }
    std::lock_guard<std::mutex> lock(g_db_mutex);
    sqlite3_stmt* st = nullptr;
    sqlite3_prepare_v2(
        g_db,
        "SELECT id, sender_id, body, ts, status, msg_type, media_url FROM messages WHERE chat_id=? ORDER BY id DESC LIMIT 200",
        -1, &st, nullptr);
    sqlite3_bind_int64(st, 1, *chat);
    std::vector<std::string> rows;
    while (sqlite3_step(st) == SQLITE_ROW) {
        const int64_t id = sqlite3_column_int64(st, 0);
        const int64_t sid = sqlite3_column_int64(st, 1);
        const char* body = reinterpret_cast<const char*>(sqlite3_column_text(st, 2));
        const int64_t ts = sqlite3_column_int64(st, 3);
        const char* status = reinterpret_cast<const char*>(sqlite3_column_text(st, 4));
        const char* msg_type = reinterpret_cast<const char*>(sqlite3_column_text(st, 5));
        const char* media_url = reinterpret_cast<const char*>(sqlite3_column_text(st, 6));
        std::ostringstream m;
        m << "{\"id\":" << id << ",\"chatId\":" << *chat << ",\"senderId\":" << sid
          << ",\"body\":\"" << JsonEscape(body ? body : "")
          << "\",\"ts\":" << ts
          << ",\"status\":\"" << JsonEscape(status ? status : "sent")
          << "\",\"msgType\":\"" << JsonEscape(msg_type ? msg_type : "text")
          << "\",\"mediaId\":\"" << JsonEscape(media_url ? media_url : "") << "\"}";
        rows.push_back(m.str());
    }
    sqlite3_finalize(st);
    std::ostringstream resp;
    resp << "{\"type\":\"chat.history\",\"chatId\":" << *chat << ",\"messages\":[";
    for (size_t i = rows.size(); i-- > 0;) {
        resp << rows[i];
        if (i > 0) {
            resp << ',';
        }
    }
    resp << "]}\n";
    SendLine(fd, resp.str());
}

void HandleDirectOpen(int fd, const std::string& line, int64_t uid) {
    const auto peer = JsonGetInt(line, "peerUserId");
    if (!peer || *peer == uid) {
        SendLine(fd, "{\"type\":\"err\",\"code\":\"bad_peer\"}\n");
        return;
    }
    const int64_t cid = EnsureDirectChat(uid, *peer);
    std::ostringstream oss;
    oss << "{\"type\":\"chat.open\",\"chatId\":" << cid << ",\"peer\":" << UserJsonById(*peer) << "}\n";
    SendLine(fd, oss.str());
}

void HandleLookupUser(int fd, const std::string& line) {
    const auto username = JsonGetString(line, "username");
    const auto id = JsonGetInt(line, "userId");
    if (username && !username->empty()) {
        std::string lower = *username;
        for (char& c : lower) {
            c = char(std::tolower(static_cast<unsigned char>(c)));
        }
        std::lock_guard<std::mutex> lock(g_db_mutex);
        sqlite3_stmt* st = nullptr;
        sqlite3_prepare_v2(g_db, "SELECT id FROM users WHERE username=?", -1, &st, nullptr);
        sqlite3_bind_text(st, 1, lower.c_str(), -1, SQLITE_TRANSIENT);
        if (sqlite3_step(st) != SQLITE_ROW) {
            sqlite3_finalize(st);
            SendLine(fd, "{\"type\":\"err\",\"code\":\"not_found\"}\n");
            return;
        }
        const int64_t uid = sqlite3_column_int64(st, 0);
        sqlite3_finalize(st);
        SendLine(fd, std::string("{\"type\":\"user.lookup\",\"user\":") + UserJsonById(uid) + "}\n");
        return;
    }
    if (id) {
        SendLine(fd, std::string("{\"type\":\"user.lookup\",\"user\":") + UserJsonById(*id) + "}\n");
        return;
    }
    SendLine(fd, "{\"type\":\"err\",\"code\":\"bad_lookup\"}\n");
}

void HandleReceipt(int fd, const std::string& line, int64_t uid) {
    const auto mid = JsonGetInt(line, "messageId");
    if (!mid) {
        return;
    }
    int64_t sender_id = 0;
    int64_t chat_id = 0;
    {
        std::lock_guard<std::mutex> lock(g_db_mutex);
        sqlite3_stmt* st = nullptr;
        sqlite3_prepare_v2(
            g_db, "SELECT sender_id, chat_id FROM messages WHERE id=?", -1, &st, nullptr);
        sqlite3_bind_int64(st, 1, *mid);
        if (sqlite3_step(st) != SQLITE_ROW) {
            sqlite3_finalize(st);
            return;
        }
        sender_id = sqlite3_column_int64(st, 0);
        chat_id = sqlite3_column_int64(st, 1);
        sqlite3_finalize(st);
        sqlite3_stmt* up = nullptr;
        sqlite3_prepare_v2(g_db, "UPDATE messages SET status='delivered' WHERE id=?", -1, &up, nullptr);
        sqlite3_bind_int64(up, 1, *mid);
        sqlite3_step(up);
        sqlite3_finalize(up);
    }
    if (sender_id != uid && sender_id != 0) {
        std::ostringstream r;
        r << "{\"type\":\"evt.receipt\",\"messageId\":" << *mid << ",\"chatId\":" << chat_id
          << ",\"status\":\"delivered\"}\n";
        SendToUser(sender_id, r.str());
    }
    (void)fd;
}

void HandleTyping(int fd, const std::string& line, int64_t uid) {
    const auto chat = JsonGetInt(line, "chatId");
    const auto isTypingVal = JsonGetInt(line, "isTyping");
    if (!chat) return;
    const bool typing = isTypingVal && *isTypingVal != 0;

    {
        std::lock_guard<std::mutex> lock(g_typing_mutex);
        if (typing) {
            g_typing[uid] = *chat;
        } else {
            g_typing.erase(uid);
        }
    }

    std::ostringstream evt;
    evt << "{\"type\":\"evt.typing\",\"userId\":" << uid << ",\"chatId\":" << *chat
        << ",\"isTyping\":" << (typing ? "true" : "false") << "}\n";
    const std::string evtStr = evt.str();

    if (*chat == kGlobalChatId) {
        BroadcastAuthenticated(evtStr, fd);
    } else {
        int64_t peer_id = 0;
        {
            std::lock_guard<std::mutex> lock(g_db_mutex);
            sqlite3_stmt* st = nullptr;
            sqlite3_prepare_v2(g_db, "SELECT u1, u2 FROM chats WHERE id=? AND kind='direct'", -1, &st, nullptr);
            sqlite3_bind_int64(st, 1, *chat);
            if (sqlite3_step(st) == SQLITE_ROW) {
                const int64_t u1 = sqlite3_column_int64(st, 0);
                const int64_t u2 = sqlite3_column_int64(st, 1);
                peer_id = (u1 == uid) ? u2 : u1;
            }
            sqlite3_finalize(st);
        }
        if (peer_id > 0) {
            SendToUser(peer_id, evtStr);
        }
    }
    (void)fd;
}

void HandleSetAvatar(int fd, const std::string& line, int64_t uid) {
    const auto mediaId = JsonGetString(line, "mediaId");
    if (!mediaId || mediaId->empty()) {
        SendLine(fd, "{\"type\":\"err\",\"code\":\"bad_media_id\"}\n");
        return;
    }
    for (char c : *mediaId) {
        if (c == '/' || c == '\\' || c == '.') {
            SendLine(fd, "{\"type\":\"err\",\"code\":\"bad_media_id\"}\n");
            return;
        }
    }
    {
        std::lock_guard<std::mutex> lock(g_db_mutex);
        sqlite3_stmt* st = nullptr;
        sqlite3_prepare_v2(g_db, "UPDATE users SET avatar_media_id=? WHERE id=?", -1, &st, nullptr);
        sqlite3_bind_text(st, 1, mediaId->c_str(), -1, SQLITE_TRANSIENT);
        sqlite3_bind_int64(st, 2, uid);
        sqlite3_step(st);
        sqlite3_finalize(st);
    }
    SendLine(fd, std::string("{\"type\":\"profile.updated\",\"user\":") + UserJsonById(uid) + "}\n");
    BroadcastAuthenticated(std::string("{\"type\":\"evt.profile\",\"user\":") + UserJsonById(uid) + "}\n", fd);
}

void HandleChatPhotoSend(int fd, const std::string& line, int64_t uid) {
    const auto mediaId = JsonGetString(line, "mediaId");
    const auto peer = JsonGetInt(line, "peerUserId");
    if (!mediaId || mediaId->empty() || !peer) {
        SendLine(fd, "{\"type\":\"err\",\"code\":\"bad_photo\"}\n");
        return;
    }
    // Basic security: reject path traversal attempts
    for (char c : *mediaId) {
        if (c == '/' || c == '\\' || c == '.') {
            SendLine(fd, "{\"type\":\"err\",\"code\":\"bad_media_id\"}\n");
            return;
        }
    }
    const int64_t chat_id = EnsureDirectChat(uid, *peer);
    const int64_t ts = std::chrono::duration_cast<std::chrono::milliseconds>(
                           std::chrono::system_clock::now().time_since_epoch())
                           .count();
    int64_t mid = 0;
    {
        std::lock_guard<std::mutex> lock(g_db_mutex);
        sqlite3_stmt* st = nullptr;
        sqlite3_prepare_v2(
            g_db,
            "INSERT INTO messages(chat_id, sender_id, body, ts, status, msg_type, media_url) VALUES(?,?,?,?,?,?,?)",
            -1, &st, nullptr);
        sqlite3_bind_int64(st, 1, chat_id);
        sqlite3_bind_int64(st, 2, uid);
        sqlite3_bind_text(st, 3, mediaId->c_str(), -1, SQLITE_TRANSIENT);
        sqlite3_bind_int64(st, 4, ts);
        sqlite3_bind_text(st, 5, "sent", -1, SQLITE_TRANSIENT);
        sqlite3_bind_text(st, 6, "photo", -1, SQLITE_TRANSIENT);
        sqlite3_bind_text(st, 7, mediaId->c_str(), -1, SQLITE_TRANSIENT);
        sqlite3_step(st);
        sqlite3_finalize(st);
        mid = sqlite3_last_insert_rowid(g_db);
    }
    auto build_evt = [&](int64_t other_uid) {
        std::ostringstream e;
        e << "{\"type\":\"evt.message\",\"message\":{\"id\":" << mid
          << ",\"chatId\":" << chat_id << ",\"senderId\":" << uid
          << ",\"otherUserId\":" << other_uid
          << ",\"msgType\":\"photo\",\"mediaId\":\"" << JsonEscape(*mediaId)
          << "\",\"body\":\"\",\"ts\":" << ts << ",\"status\":\"sent\"}}\n";
        return e.str();
    };
    SendToUser(*peer, build_evt(uid));
    SendLine(fd, build_evt(*peer));
}

// ===== HTTP MEDIA SERVER =====

void EnsureMediaDir() {
    mkdir(kMediaDir.c_str(), 0755);
}

struct HttpReq {
    std::string method;
    std::string path;
    std::unordered_map<std::string, std::string> headers;
    std::string body;
    bool valid = false;
};

HttpReq ParseHttpRequest(int fd) {
    HttpReq req;
    std::string raw;
    char buf[8192];
    while (raw.find("\r\n\r\n") == std::string::npos) {
        ssize_t n = recv(fd, buf, sizeof(buf) - 1, 0);
        if (n <= 0) return req;
        raw.append(buf, static_cast<size_t>(n));
        if (raw.size() > 131072) return req;
    }
    size_t lineEnd = raw.find("\r\n");
    if (lineEnd == std::string::npos) return req;
    std::string requestLine = raw.substr(0, lineEnd);
    size_t sp1 = requestLine.find(' ');
    size_t sp2 = requestLine.rfind(' ');
    if (sp1 == std::string::npos || sp2 == sp1) return req;
    req.method = requestLine.substr(0, sp1);
    req.path = requestLine.substr(sp1 + 1, sp2 - sp1 - 1);
    size_t headerEnd = raw.find("\r\n\r\n");
    size_t pos = lineEnd + 2;
    while (pos < headerEnd) {
        size_t nl = raw.find("\r\n", pos);
        if (nl == std::string::npos) break;
        std::string hline = raw.substr(pos, nl - pos);
        size_t colon = hline.find(": ");
        if (colon != std::string::npos) {
            std::string key = hline.substr(0, colon);
            for (char& c : key) c = char(std::tolower(static_cast<unsigned char>(c)));
            req.headers[key] = hline.substr(colon + 2);
        }
        pos = nl + 2;
    }
    pos = headerEnd + 4;
    auto clIt = req.headers.find("content-length");
    if (clIt != req.headers.end()) {
        size_t contentLength = 0;
        try { contentLength = std::stoul(clIt->second); } catch (...) {}
        if (contentLength > 10 * 1024 * 1024) return req; // max 10 MB
        req.body = raw.substr(pos);
        while (req.body.size() < contentLength) {
            ssize_t n = recv(fd, buf, sizeof(buf) - 1, 0);
            if (n <= 0) break;
            req.body.append(buf, static_cast<size_t>(n));
        }
    }
    req.valid = true;
    return req;
}

void SendHttpResp(int fd, int status, const std::string& ct, const std::string& body) {
    const char* st = (status == 200) ? "OK" : (status == 404) ? "Not Found"
                   : (status == 401) ? "Unauthorized" : "Bad Request";
    std::ostringstream h;
    h << "HTTP/1.1 " << status << " " << st << "\r\n"
      << "Content-Type: " << ct << "\r\n"
      << "Content-Length: " << body.size() << "\r\n"
      << "Access-Control-Allow-Origin: *\r\n"
      << "Connection: close\r\n\r\n";
    std::string hdr = h.str();
    send(fd, hdr.data(), hdr.size(), MSG_NOSIGNAL);
    send(fd, body.data(), body.size(), MSG_NOSIGNAL);
}

void HandleMediaClient(int fd) {
    auto req = ParseHttpRequest(fd);
    if (!req.valid) { close(fd); return; }

    if (req.method == "OPTIONS") {
        const std::string resp =
            "HTTP/1.1 200 OK\r\n"
            "Access-Control-Allow-Origin: *\r\n"
            "Access-Control-Allow-Methods: POST, GET\r\n"
            "Access-Control-Allow-Headers: Authorization, Content-Type\r\n"
            "Content-Length: 0\r\n\r\n";
        send(fd, resp.data(), resp.size(), MSG_NOSIGNAL);
        close(fd); return;
    }

    if (req.method == "POST" && req.path == "/upload") {
        std::string token;
        auto authIt = req.headers.find("authorization");
        if (authIt != req.headers.end() && authIt->second.substr(0, 7) == "Bearer ") {
            token = authIt->second.substr(7);
        }
        if (!ValidateToken(token)) {
            SendHttpResp(fd, 401, "application/json", "{\"error\":\"unauthorized\"}");
            close(fd); return;
        }
        if (req.body.empty()) {
            SendHttpResp(fd, 400, "application/json", "{\"error\":\"empty_body\"}");
            close(fd); return;
        }
        const std::string filename = RandomHex(16) + ".jpg";
        std::ofstream f(kMediaDir + filename, std::ios::binary);
        if (!f) {
            SendHttpResp(fd, 500, "application/json", "{\"error\":\"write_failed\"}");
            close(fd); return;
        }
        f.write(req.body.data(), static_cast<std::streamsize>(req.body.size()));
        f.close();
        SendHttpResp(fd, 200, "application/json", "{\"mediaId\":\"" + filename + "\"}");

    } else if (req.method == "GET" && req.path.size() > 7 && req.path.substr(0, 7) == "/media/") {
        const std::string filename = req.path.substr(7);
        // Reject path traversal
        if (filename.find('/') != std::string::npos || filename.find("..") != std::string::npos) {
            SendHttpResp(fd, 400, "application/json", "{\"error\":\"bad_path\"}");
            close(fd); return;
        }
        std::ifstream f(kMediaDir + filename, std::ios::binary | std::ios::ate);
        if (!f) {
            SendHttpResp(fd, 404, "application/json", "{\"error\":\"not_found\"}");
            close(fd); return;
        }
        std::streamsize sz = f.tellg();
        f.seekg(0, std::ios::beg);
        std::string data(static_cast<size_t>(sz), '\0');
        f.read(data.data(), sz);
        SendHttpResp(fd, 200, "image/jpeg", data);
    } else {
        SendHttpResp(fd, 404, "application/json", "{\"error\":\"not_found\"}");
    }
    close(fd);
}

void RunMediaServer(int port) {
    EnsureMediaDir();
    int sfd = socket(AF_INET, SOCK_STREAM, 0);
    if (sfd < 0) { std::cerr << "Media server socket failed\n"; return; }
    int opt = 1;
    setsockopt(sfd, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));
    sockaddr_in addr{};
    addr.sin_family = AF_INET;
    addr.sin_addr.s_addr = INADDR_ANY;
    addr.sin_port = htons(static_cast<uint16_t>(port));
    if (bind(sfd, reinterpret_cast<sockaddr*>(&addr), sizeof(addr)) < 0) {
        std::cerr << "Media server bind failed on port " << port << "\n";
        close(sfd); return;
    }
    listen(sfd, 16);
    std::cout << "Media HTTP server listening on port " << port << "\n";
    while (g_running) {
        sockaddr_in ca{};
        socklen_t cl = sizeof(ca);
        int cfd = accept(sfd, reinterpret_cast<sockaddr*>(&ca), &cl);
        if (cfd < 0) { if (errno == EINTR) continue; break; }
        std::thread(HandleMediaClient, cfd).detach();
    }
    close(sfd);
}

void HandleLine(int fd, const std::string& line) {
    const auto type = JsonGetString(line, "type");
    if (!type) {
        return;
    }

    const bool authed = [&]() -> bool {
        std::lock_guard<std::mutex> lock(g_net_mutex);
        return g_fd_user.count(fd) > 0;
    }();

    if (!authed) {
        if (*type == "auth.login") {
            HandleAuthLogin(fd, line);
            return;
        }
        if (*type == "auth.register") {
            HandleAuthRegister(fd, line);
            return;
        }
        if (*type == "auth.resume") {
            const auto tok = JsonGetString(line, "token");
            if (!tok || tok->empty()) {
                SendLine(fd, "{\"type\":\"err\",\"code\":\"bad_token\"}\n");
                return;
            }
            const auto uid = ValidateToken(*tok);
            if (!uid) {
                SendLine(fd, "{\"type\":\"err\",\"code\":\"session_expired\"}\n");
                return;
            }
            UnregisterClient(fd);
            RegisterSessionForFd(fd, *uid, *tok);
            std::ostringstream oss;
            oss << "{\"type\":\"auth.session\",\"token\":\"" << JsonEscape(*tok) << "\",\"user\":"
                << UserJsonById(*uid) << "}\n";
            SendLine(fd, oss.str());
            PushPresenceSnapshot();
            return;
        }
        SendLine(fd, "{\"type\":\"err\",\"code\":\"auth_required\"}\n");
        return;
    }

    const auto uid = AuthUidForLine(line, fd);
    if (!uid) {
        SendLine(fd, "{\"type\":\"err\",\"code\":\"bad_token\"}\n");
        return;
    }

    if (*type == "profile.update") {
        HandleProfileUpdate(fd, line, *uid);
    } else if (*type == "friends.add") {
        HandleFriendsAdd(fd, line, *uid);
    } else if (*type == "friends.list") {
        SendFriendsList(fd, *uid);
    } else if (*type == "chat.global.send") {
        HandleChatGlobal(fd, line, *uid);
    } else if (*type == "chat.direct.send") {
        HandleChatDirect(fd, line, *uid);
    } else if (*type == "chat.history") {
        HandleHistory(fd, line, *uid);
    } else if (*type == "chat.direct.open") {
        HandleDirectOpen(fd, line, *uid);
    } else if (*type == "chat.photo.send") {
        HandleChatPhotoSend(fd, line, *uid);
    } else if (*type == "user.lookup") {
        HandleLookupUser(fd, line);
    } else if (*type == "chat.receipt") {
        HandleReceipt(fd, line, *uid);
    } else if (*type == "typing") {
        HandleTyping(fd, line, *uid);
    } else if (*type == "profile.set_avatar") {
        HandleSetAvatar(fd, line, *uid);
    } else if (*type == "ping") {
        SendLine(fd, "{\"type\":\"pong\"}\n");
    } else {
        SendLine(fd, "{\"type\":\"err\",\"code\":\"unknown_type\"}\n");
    }
}

void HandleClient(int client_fd, sockaddr_in client_addr) {
    char ip[INET_ADDRSTRLEN] = {0};
    inet_ntop(AF_INET, &client_addr.sin_addr, ip, sizeof(ip));
    std::cout << "Client connected: " << ip << ":" << ntohs(client_addr.sin_port) << "\n";

    std::string pending;
    std::vector<char> buffer(kBufferSize);

    while (g_running) {
        const ssize_t read_bytes = recv(client_fd, buffer.data(), buffer.size(), 0);
        if (read_bytes == 0) {
            break;
        }
        if (read_bytes < 0) {
            if (errno == EINTR) {
                continue;
            }
            std::cerr << "recv() failed: " << std::strerror(errno) << "\n";
            break;
        }

        pending.append(buffer.data(), static_cast<size_t>(read_bytes));

        size_t pos = 0;
        while ((pos = pending.find('\n')) != std::string::npos) {
            std::string ln = pending.substr(0, pos);
            pending.erase(0, pos + 1);
            while (!ln.empty() && (ln.back() == '\r' || ln.back() == ' ')) {
                ln.pop_back();
            }
            if (!ln.empty()) {
                HandleLine(client_fd, ln);
            }
        }
    }

    UnregisterClient(client_fd);
    close(client_fd);
    std::cout << "Client disconnected: " << ip << ":" << ntohs(client_addr.sin_port) << "\n";
    PushPresenceSnapshot();
}

int CreateServerSocket(int port) {
    int server_fd = socket(AF_INET, SOCK_STREAM, 0);
    if (server_fd < 0) {
        throw std::runtime_error("socket() failed");
    }

    int opt = 1;
    if (setsockopt(server_fd, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt)) < 0) {
        close(server_fd);
        throw std::runtime_error("setsockopt() failed");
    }

    sockaddr_in server_addr {};
    server_addr.sin_family = AF_INET;
    server_addr.sin_addr.s_addr = INADDR_ANY;
    server_addr.sin_port = htons(static_cast<uint16_t>(port));

    if (bind(server_fd, reinterpret_cast<sockaddr*>(&server_addr), sizeof(server_addr)) < 0) {
        close(server_fd);
        throw std::runtime_error("bind() failed");
    }

    if (listen(server_fd, kBacklog) < 0) {
        close(server_fd);
        throw std::runtime_error("listen() failed");
    }

    return server_fd;
}

void TryPublishMdns(int port) {
    const char* dis = std::getenv("MASE_NO_MDNS");
    if (dis && dis[0] == '1') {
        return;
    }
    const pid_t pid = fork();
    if (pid == 0) {
        std::string ps = std::to_string(port);
        execlp("avahi-publish", "avahi-publish", "-s", "mase", "_mase._tcp", ps.c_str(), nullptr);
        _exit(127);
    }
    if (pid > 0) {
        std::cout << "[mDNS] started avahi-publish sidecar (pid " << pid << "). If it fails, install "
                     "avahi-utils.\n";
    }
}

// UDP beacon: broadcasts {"mase":1,"port":5555} on port 5553 every 3 seconds
// so clients on the same LAN can discover the server without mDNS.
void RunUdpBeacon(int tcpPort) {
    const int udpPort = tcpPort - 2;  // 5555 -> 5553
    int fd = socket(AF_INET, SOCK_DGRAM, 0);
    if (fd < 0) { std::cerr << "[beacon] socket failed\n"; return; }

    int yes = 1;
    setsockopt(fd, SOL_SOCKET, SO_BROADCAST, &yes, sizeof(yes));
    setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &yes, sizeof(yes));

    sockaddr_in dst{};
    dst.sin_family = AF_INET;
    dst.sin_port   = htons(udpPort);
    dst.sin_addr.s_addr = INADDR_BROADCAST;

    std::string msg = std::string("{\"mase\":1,\"port\":") + std::to_string(tcpPort) + "}";
    std::cout << "[beacon] UDP broadcast on port " << udpPort << "\n";

    while (g_running) {
        sendto(fd, msg.c_str(), msg.size(), 0,
               reinterpret_cast<sockaddr*>(&dst), sizeof(dst));
        std::this_thread::sleep_for(std::chrono::seconds(3));
    }
    close(fd);
}

}  // namespace

int main(int argc, char** argv) {
    std::signal(SIGINT, HandleSignal);
    std::signal(SIGTERM, HandleSignal);

    int port = kDefaultPort;
    std::string dbpath = "mase.sqlite";
    if (argc >= 2) {
        port = std::atoi(argv[1]);
    }
    if (argc >= 3) {
        dbpath = argv[2];
    }

    if (!DbInit(dbpath)) {
        return 1;
    }

    TryPublishMdns(port);
    std::thread(RunMediaServer, port + 1).detach();
    std::thread(RunUdpBeacon, port).detach();

    try {
        const int server_fd = CreateServerSocket(port);
        std::cout << "Mase server listening on 0.0.0.0:" << port << " db=" << dbpath << "\n";

        while (g_running) {
            sockaddr_in client_addr {};
            socklen_t client_len = sizeof(client_addr);
            int client_fd = accept(server_fd, reinterpret_cast<sockaddr*>(&client_addr), &client_len);
            if (client_fd < 0) {
                if (errno == EINTR) {
                    continue;
                }
                std::cerr << "accept() failed: " << std::strerror(errno) << "\n";
                continue;
            }

            std::thread(HandleClient, client_fd, client_addr).detach();
        }

        close(server_fd);
        std::cout << "Server stopped\n";
    } catch (const std::exception& ex) {
        std::cerr << "Fatal error: " << ex.what() << "\n";
        sqlite3_close(g_db);
        return 1;
    }

    sqlite3_close(g_db);
    return 0;
}
