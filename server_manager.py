#!/usr/bin/env python3
"""
Mase Server Manager — GUI для управления сервером Mase Messenger
Запуск: python3 server_manager.py
"""

import sys
import os
import signal
import subprocess
import socket
import sqlite3
import time
import threading
from datetime import datetime
from pathlib import Path

from PyQt5.QtWidgets import (
    QApplication, QMainWindow, QWidget, QVBoxLayout, QHBoxLayout,
    QPushButton, QLabel, QTextEdit, QGroupBox, QGridLayout,
    QSplitter, QFrame, QScrollArea, QStatusBar, QSizePolicy,
    QSpacerItem
)
from PyQt5.QtCore import Qt, QTimer, QThread, pyqtSignal, QProcess
from PyQt5.QtGui import QFont, QColor, QPalette, QTextCursor, QIcon, QPixmap

# ── Пути ─────────────────────────────────────────────────────────────────────
BASE_DIR   = Path(__file__).parent
SERVER_BIN = BASE_DIR / "cpp-server" / "build" / "mase_server"
MEDIA_DIR  = BASE_DIR / "cpp-server" / "build" / "media"
DB_PATH    = BASE_DIR / "cpp-server" / "build" / "mase.sqlite"
TCP_PORT   = 5555
MEDIA_PORT = TCP_PORT + 1


# ── Поток чтения stdout сервера ───────────────────────────────────────────────
class ServerOutputReader(QThread):
    line_received = pyqtSignal(str)

    def __init__(self, process: QProcess):
        super().__init__()
        self.process = process

    def run(self):
        pass  # QProcess читается через сигналы


# ── Статус-индикатор ──────────────────────────────────────────────────────────
class StatusDot(QLabel):
    def __init__(self):
        super().__init__()
        self.setFixedSize(14, 14)
        self.set_stopped()

    def set_running(self):
        self.setStyleSheet(
            "background:#2ecc71; border-radius:7px; border:2px solid #27ae60;"
        )
        self.setToolTip("Сервер запущен")

    def set_stopped(self):
        self.setStyleSheet(
            "background:#e74c3c; border-radius:7px; border:2px solid #c0392b;"
        )
        self.setToolTip("Сервер остановлен")

    def set_starting(self):
        self.setStyleSheet(
            "background:#f39c12; border-radius:7px; border:2px solid #d35400;"
        )
        self.setToolTip("Запускается...")


# ── Карточка с информацией ────────────────────────────────────────────────────
class InfoCard(QGroupBox):
    def __init__(self, title: str):
        super().__init__(title)
        self.setStyleSheet("""
            QGroupBox {
                font-weight: bold;
                font-size: 12px;
                border: 1px solid #3a3a4a;
                border-radius: 8px;
                margin-top: 8px;
                padding: 8px;
                background: #1e1e2e;
                color: #cdd6f4;
            }
            QGroupBox::title {
                subcontrol-origin: margin;
                left: 10px;
                padding: 0 4px;
                color: #89b4fa;
            }
        """)
        self.grid = QGridLayout()
        self.grid.setColumnStretch(1, 1)
        self.grid.setSpacing(6)
        self.setLayout(self.grid)
        self._row = 0
        self._values: dict[str, QLabel] = {}

    def add_row(self, key: str, default: str = "—"):
        key_lbl = QLabel(key + ":")
        key_lbl.setStyleSheet("color:#a6adc8; font-size:11px;")
        val_lbl = QLabel(default)
        val_lbl.setStyleSheet("color:#cdd6f4; font-size:11px; font-weight:600;")
        val_lbl.setTextInteractionFlags(Qt.TextSelectableByMouse)
        self.grid.addWidget(key_lbl, self._row, 0)
        self.grid.addWidget(val_lbl, self._row, 1)
        self._values[key] = val_lbl
        self._row += 1

    def set(self, key: str, value: str, color: str | None = None):
        lbl = self._values.get(key)
        if lbl:
            lbl.setText(value)
            if color:
                lbl.setStyleSheet(f"color:{color}; font-size:11px; font-weight:600;")
            else:
                lbl.setStyleSheet("color:#cdd6f4; font-size:11px; font-weight:600;")


# ── Главное окно ──────────────────────────────────────────────────────────────
class ServerManager(QMainWindow):
    def __init__(self):
        super().__init__()
        self.process: QProcess | None = None
        self._start_time: float | None = None
        self._log_lines = 0

        self.setWindowTitle("Mase Server Manager")
        self.setMinimumSize(900, 640)
        self.resize(1060, 720)
        self._apply_dark_theme()
        self._build_ui()

        # Таймер обновления статистики (каждые 2с)
        self.stats_timer = QTimer(self)
        self.stats_timer.timeout.connect(self._refresh_stats)
        self.stats_timer.start(2000)

        self._refresh_stats()
        self._log_system("Mase Server Manager запущен")
        self._log_system(f"Бинарник: {SERVER_BIN}")
        self._log_system(f"База данных: {DB_PATH}")

    # ── Тема ──────────────────────────────────────────────────────────────────
    def _apply_dark_theme(self):
        self.setStyleSheet("""
            QMainWindow, QWidget {
                background: #181825;
                color: #cdd6f4;
                font-family: 'JetBrains Mono', 'Fira Code', 'Consolas', monospace;
            }
            QPushButton {
                border: none;
                border-radius: 8px;
                padding: 8px 18px;
                font-size: 12px;
                font-weight: bold;
            }
            QPushButton:disabled { opacity: 0.4; background: #313244; color: #6c7086; }
            QStatusBar { background: #11111b; color: #6c7086; font-size: 10px; }
            QScrollBar:vertical { background:#181825; width:8px; border-radius:4px; }
            QScrollBar::handle:vertical { background:#45475a; border-radius:4px; min-height:20px; }
            QScrollBar::add-line:vertical, QScrollBar::sub-line:vertical { height:0; }
            QSplitter::handle { background:#313244; }
        """)

    # ── UI ────────────────────────────────────────────────────────────────────
    def _build_ui(self):
        central = QWidget()
        self.setCentralWidget(central)
        root = QVBoxLayout(central)
        root.setSpacing(10)
        root.setContentsMargins(14, 14, 14, 10)

        # Заголовок
        header = QHBoxLayout()
        title = QLabel("⚡ Mase Server Manager")
        title.setStyleSheet("font-size:18px; font-weight:bold; color:#89b4fa;")
        header.addWidget(title)
        header.addStretch()

        self.status_dot = StatusDot()
        self.status_label = QLabel("Остановлен")
        self.status_label.setStyleSheet("color:#e74c3c; font-size:12px; font-weight:bold;")
        header.addWidget(self.status_dot)
        header.addSpacing(6)
        header.addWidget(self.status_label)
        root.addLayout(header)

        # Кнопки управления
        btn_row = QHBoxLayout()
        btn_row.setSpacing(10)

        self.btn_start = self._make_btn("▶  Запустить", "#2ecc71", "#27ae60", self._start_server)
        self.btn_stop  = self._make_btn("■  Остановить", "#e74c3c", "#c0392b", self._stop_server)
        self.btn_restart = self._make_btn("↺  Перезапустить", "#f39c12", "#d35400", self._restart_server)
        self.btn_clear = self._make_btn("🗑  Очистить лог", "#45475a", "#313244", self._clear_log)

        self.btn_stop.setEnabled(False)
        self.btn_restart.setEnabled(False)

        btn_row.addWidget(self.btn_start)
        btn_row.addWidget(self.btn_stop)
        btn_row.addWidget(self.btn_restart)
        btn_row.addStretch()
        btn_row.addWidget(self.btn_clear)
        root.addLayout(btn_row)

        # Разделитель
        line = QFrame()
        line.setFrameShape(QFrame.HLine)
        line.setStyleSheet("color:#313244;")
        root.addWidget(line)

        # Основная панель: инфо слева, лог справа
        splitter = QSplitter(Qt.Horizontal)
        splitter.setHandleWidth(6)

        # ── Левая панель ──────────────────────────────────────────────────────
        left = QWidget()
        left.setMaximumWidth(300)
        left_layout = QVBoxLayout(left)
        left_layout.setSpacing(10)
        left_layout.setContentsMargins(0, 0, 6, 0)

        # Карточка: сервер
        self.card_server = InfoCard("Сервер")
        self.card_server.add_row("Статус")
        self.card_server.add_row("PID")
        self.card_server.add_row("TCP порт")
        self.card_server.add_row("Медиа порт")
        self.card_server.add_row("Аптайм")
        self.card_server.add_row("Переменная OTP")
        left_layout.addWidget(self.card_server)

        # Карточка: сеть
        self.card_net = InfoCard("Сеть")
        self.card_net.add_row("Локальный IP")
        self.card_net.add_row("TCP доступен")
        self.card_net.add_row("Медиа доступен")
        left_layout.addWidget(self.card_net)

        # Карточка: БД
        self.card_db = InfoCard("База данных")
        self.card_db.add_row("Пользователей")
        self.card_db.add_row("Сообщений")
        self.card_db.add_row("Чатов")
        self.card_db.add_row("Медиа файлов")
        self.card_db.add_row("Размер БД")
        left_layout.addWidget(self.card_db)

        left_layout.addStretch()
        splitter.addWidget(left)

        # ── Правая панель: лог ────────────────────────────────────────────────
        right = QWidget()
        right_layout = QVBoxLayout(right)
        right_layout.setContentsMargins(6, 0, 0, 0)
        right_layout.setSpacing(4)

        log_header = QHBoxLayout()
        log_title = QLabel("Логи сервера")
        log_title.setStyleSheet("font-size:13px; font-weight:bold; color:#89b4fa;")
        self.log_count_lbl = QLabel("0 строк")
        self.log_count_lbl.setStyleSheet("color:#6c7086; font-size:10px;")
        log_header.addWidget(log_title)
        log_header.addStretch()
        log_header.addWidget(self.log_count_lbl)
        right_layout.addLayout(log_header)

        self.log_view = QTextEdit()
        self.log_view.setReadOnly(True)
        self.log_view.setStyleSheet("""
            QTextEdit {
                background: #11111b;
                color: #cdd6f4;
                border: 1px solid #313244;
                border-radius: 8px;
                padding: 8px;
                font-family: 'JetBrains Mono', 'Fira Code', 'Consolas', monospace;
                font-size: 11px;
                line-height: 1.4;
            }
        """)
        right_layout.addWidget(self.log_view)
        splitter.addWidget(right)

        splitter.setSizes([270, 700])
        root.addWidget(splitter)

        # Статусная строка
        self.status_bar = QStatusBar()
        self.setStatusBar(self.status_bar)
        self.status_bar.showMessage("Готов к работе")

    def _make_btn(self, text, bg, bg_hover, callback):
        btn = QPushButton(text)
        btn.setStyleSheet(f"""
            QPushButton {{
                background: {bg};
                color: #11111b;
                font-weight: bold;
                font-size: 12px;
                border-radius: 8px;
                padding: 8px 20px;
            }}
            QPushButton:hover {{ background: {bg_hover}; }}
            QPushButton:pressed {{ background: {bg_hover}; padding: 9px 19px 7px 21px; }}
            QPushButton:disabled {{ background: #313244; color: #585b70; }}
        """)
        btn.clicked.connect(callback)
        return btn

    # ── Управление сервером ───────────────────────────────────────────────────
    def _start_server(self):
        if self.process and self.process.state() != QProcess.NotRunning:
            return

        MEDIA_DIR.mkdir(parents=True, exist_ok=True)

        env = {**os.environ, "MASE_DEV_RETURN_OTP": "1"}

        self.process = QProcess(self)
        self.process.setWorkingDirectory(str(SERVER_BIN.parent))
        self.process.readyReadStandardOutput.connect(self._on_stdout)
        self.process.readyReadStandardError.connect(self._on_stderr)
        self.process.started.connect(self._on_started)
        self.process.finished.connect(self._on_finished)

        # Env
        from PyQt5.QtCore import QProcessEnvironment
        qenv = QProcessEnvironment.systemEnvironment()
        qenv.insert("MASE_DEV_RETURN_OTP", "1")
        self.process.setProcessEnvironment(qenv)

        self.status_dot.set_starting()
        self.status_label.setText("Запускается...")
        self.status_label.setStyleSheet("color:#f39c12; font-size:12px; font-weight:bold;")

        self.process.start(str(SERVER_BIN), [str(TCP_PORT), str(DB_PATH)])
        self._log_system(f"Запуск: {SERVER_BIN} {TCP_PORT} {DB_PATH}")

    def _stop_server(self):
        if not self.process:
            return
        self._log_system("Остановка сервера (SIGTERM)...")
        self.process.terminate()
        QTimer.singleShot(3000, self._force_kill)

    def _force_kill(self):
        if self.process and self.process.state() != QProcess.NotRunning:
            self._log_system("Принудительное завершение (SIGKILL)...")
            self.process.kill()

    def _restart_server(self):
        self._log_system("Перезапуск сервера...")
        if self.process and self.process.state() != QProcess.NotRunning:
            self.process.finished.connect(lambda: QTimer.singleShot(500, self._start_server))
            self._stop_server()
        else:
            self._start_server()

    # ── События процесса ──────────────────────────────────────────────────────
    def _on_started(self):
        self._start_time = time.time()
        pid = self.process.processId()
        self.status_dot.set_running()
        self.status_label.setText("Запущен")
        self.status_label.setStyleSheet("color:#2ecc71; font-size:12px; font-weight:bold;")
        self.btn_start.setEnabled(False)
        self.btn_stop.setEnabled(True)
        self.btn_restart.setEnabled(True)
        self.card_server.set("Статус", "● Запущен", "#2ecc71")
        self.card_server.set("PID", str(pid))
        self.card_server.set("TCP порт", str(TCP_PORT))
        self.card_server.set("Медиа порт", str(MEDIA_PORT))
        self.card_server.set("Переменная OTP", "MASE_DEV_RETURN_OTP=1", "#f9e2af")
        self._log_system(f"Сервер запущен. PID={pid}, порт={TCP_PORT}, медиа={MEDIA_PORT}")
        self.status_bar.showMessage(f"Сервер запущен | PID {pid} | порт {TCP_PORT}")

    def _on_finished(self, exit_code, exit_status):
        self._start_time = None
        self.status_dot.set_stopped()
        self.status_label.setText("Остановлен")
        self.status_label.setStyleSheet("color:#e74c3c; font-size:12px; font-weight:bold;")
        self.btn_start.setEnabled(True)
        self.btn_stop.setEnabled(False)
        self.btn_restart.setEnabled(False)
        self.card_server.set("Статус", "● Остановлен", "#e74c3c")
        self.card_server.set("PID", "—")
        self.card_server.set("Аптайм", "—")
        self._log_system(f"Сервер завершён. Код={exit_code}")
        self.status_bar.showMessage(f"Сервер остановлен (код {exit_code})")

    def _on_stdout(self):
        data = self.process.readAllStandardOutput().data().decode(errors="replace")
        for line in data.splitlines():
            if line.strip():
                self._log_server(line)

    def _on_stderr(self):
        data = self.process.readAllStandardError().data().decode(errors="replace")
        for line in data.splitlines():
            if line.strip():
                self._log_server(line, error=True)

    # ── Логирование ───────────────────────────────────────────────────────────
    def _log_server(self, line: str, error: bool = False):
        ts = datetime.now().strftime("%H:%M:%S")
        color = "#f38ba8" if error else "#a6e3a1"
        prefix_color = "#6c7086"
        self.log_view.append(
            f'<span style="color:{prefix_color}">[{ts}]</span> '
            f'<span style="color:{color}">{self._escape(line)}</span>'
        )
        self._scroll_log()

    def _log_system(self, line: str):
        ts = datetime.now().strftime("%H:%M:%S")
        self.log_view.append(
            f'<span style="color:#6c7086">[{ts}]</span> '
            f'<span style="color:#89b4fa">▸ {self._escape(line)}</span>'
        )
        self._scroll_log()

    def _scroll_log(self):
        self._log_lines += 1
        self.log_count_lbl.setText(f"{self._log_lines} строк")
        cursor = self.log_view.textCursor()
        cursor.movePosition(QTextCursor.End)
        self.log_view.setTextCursor(cursor)

    def _clear_log(self):
        self.log_view.clear()
        self._log_lines = 0
        self.log_count_lbl.setText("0 строк")
        self._log_system("Лог очищен")

    @staticmethod
    def _escape(s: str) -> str:
        return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")

    # ── Статистика ────────────────────────────────────────────────────────────
    def _refresh_stats(self):
        # Аптайм
        if self._start_time:
            elapsed = int(time.time() - self._start_time)
            h, rem = divmod(elapsed, 3600)
            m, s = divmod(rem, 60)
            uptime = f"{h:02d}:{m:02d}:{s:02d}"
            self.card_server.set("Аптайм", uptime, "#a6e3a1")

        # Локальный IP
        local_ip = self._get_local_ip()
        self.card_net.set("Локальный IP", local_ip)

        # Проверка портов
        tcp_ok = self._check_port(TCP_PORT)
        media_ok = self._check_port(MEDIA_PORT)
        self.card_net.set("TCP доступен",
            "● Да" if tcp_ok else "○ Нет",
            "#2ecc71" if tcp_ok else "#e74c3c")
        self.card_net.set("Медиа доступен",
            "● Да" if media_ok else "○ Нет",
            "#2ecc71" if media_ok else "#e74c3c")

        # БД
        self._refresh_db_stats()

    def _refresh_db_stats(self):
        if not DB_PATH.exists():
            self.card_db.set("Пользователей", "нет БД", "#f38ba8")
            return
        try:
            con = sqlite3.connect(str(DB_PATH))
            cur = con.cursor()
            users = self._query_count(cur, "SELECT COUNT(*) FROM users")
            msgs  = self._query_count(cur, "SELECT COUNT(*) FROM messages")
            chats = self._query_count(cur, "SELECT COUNT(*) FROM chats")
            con.close()
            size_kb = DB_PATH.stat().st_size // 1024
            self.card_db.set("Пользователей", str(users))
            self.card_db.set("Сообщений", str(msgs))
            self.card_db.set("Чатов", str(chats))
            self.card_db.set("Размер БД", f"{size_kb} KB")
        except Exception as e:
            self.card_db.set("Пользователей", f"ошибка: {e}", "#f38ba8")

        # Медиа файлов
        if MEDIA_DIR.exists():
            media_count = len(list(MEDIA_DIR.iterdir()))
            self.card_db.set("Медиа файлов", str(media_count))
        else:
            self.card_db.set("Медиа файлов", "0")

    @staticmethod
    def _query_count(cur, sql: str) -> int:
        try:
            cur.execute(sql)
            return cur.fetchone()[0]
        except Exception:
            return 0

    @staticmethod
    def _get_local_ip() -> str:
        try:
            s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            s.connect(("8.8.8.8", 80))
            ip = s.getsockname()[0]
            s.close()
            return ip
        except Exception:
            return "недоступен"

    @staticmethod
    def _check_port(port: int) -> bool:
        try:
            s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            s.settimeout(0.3)
            s.connect(("127.0.0.1", port))
            s.close()
            return True
        except Exception:
            return False

    def closeEvent(self, event):
        if self.process and self.process.state() != QProcess.NotRunning:
            self.process.kill()
        event.accept()


# ── Точка входа ───────────────────────────────────────────────────────────────
if __name__ == "__main__":
    app = QApplication(sys.argv)
    app.setApplicationName("Mase Server Manager")
    window = ServerManager()
    window.show()
    sys.exit(app.exec_())
