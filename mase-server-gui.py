#!/usr/bin/env python3
"""Mase Server — графический интерфейс управления сервером."""

import gi
gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")

from gi.repository import Gtk, Adw, GLib, Gio

import subprocess
import threading
import socket
import signal
import os
import sys
from datetime import datetime
from pathlib import Path

# ── Пути ────────────────────────────────────────────────────────────────────
SCRIPT_DIR  = Path(__file__).parent.resolve()
SERVER_BIN  = SCRIPT_DIR / "cpp-server" / "build" / "mase_server"
DATA_DIR    = SCRIPT_DIR / "data"
DEFAULT_DB  = DATA_DIR / "mase.sqlite"


def get_local_ip() -> str:
    try:
        s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        s.connect(("8.8.8.8", 80))
        ip = s.getsockname()[0]
        s.close()
        return ip
    except Exception:
        return "127.0.0.1"


def find_orphan_server() -> int | None:
    """Возвращает PID процесса mase_server если он уже запущен без GUI."""
    try:
        out = subprocess.check_output(
            ["pgrep", "-x", "mase_server"], text=True
        ).strip()
        pids = [int(p) for p in out.splitlines() if p.strip()]
        return pids[0] if pids else None
    except subprocess.CalledProcessError:
        return None


# ── Главное окно ─────────────────────────────────────────────────────────────
class MaseServerWindow(Adw.ApplicationWindow):
    def __init__(self, app):
        super().__init__(application=app, title="Mase Server")
        self.set_default_size(520, 680)
        self.set_resizable(True)

        self._proc: subprocess.Popen | None = None
        self._start_time: datetime | None = None
        self._clients: int = 0
        self._uptime_timer_id: int | None = None
        self._log_lines: list[str] = []

        self._build_ui()
        # Проверяем есть ли уже запущенный сервер
        GLib.idle_add(self._check_orphan_on_start)

    # ── Построение интерфейса ────────────────────────────────────────────────
    def _build_ui(self):
        # Toolbar + content box
        toolbar_view = Adw.ToolbarView()
        self.set_content(toolbar_view)

        # Header bar
        header = Adw.HeaderBar()
        header.set_title_widget(Adw.WindowTitle(
            title="Mase Server",
            subtitle="Локальная сеть"
        ))
        toolbar_view.add_top_bar(header)

        # Main scrollable content
        scroll = Gtk.ScrolledWindow(vexpand=True)
        scroll.set_policy(Gtk.PolicyType.NEVER, Gtk.PolicyType.AUTOMATIC)
        toolbar_view.set_content(scroll)

        root = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=0)
        root.set_margin_top(16)
        root.set_margin_bottom(24)
        root.set_margin_start(16)
        root.set_margin_end(16)
        scroll.set_child(root)

        # ── Статус-карточка ──────────────────────────────────────────────────
        status_card = Adw.Clamp(maximum_size=480)
        root.append(status_card)

        status_box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=16)
        status_box.add_css_class("card")
        status_box.set_margin_top(8)
        status_box.set_margin_bottom(4)
        status_card.set_child(status_box)

        # Индикатор статуса
        indicator_row = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=10)
        indicator_row.set_halign(Gtk.Align.CENTER)
        indicator_row.set_margin_top(20)
        indicator_row.set_margin_start(20)
        indicator_row.set_margin_end(20)
        status_box.append(indicator_row)

        self._status_dot = Gtk.Label(label="●")
        self._status_dot.set_markup('<span foreground="#888888" font="18">●</span>')
        indicator_row.append(self._status_dot)

        self._status_label = Gtk.Label(label="Остановлен")
        self._status_label.add_css_class("title-2")
        indicator_row.append(self._status_label)

        # Большая кнопка Запустить/Остановить
        btn_box = Gtk.Box(spacing=8)
        btn_box.set_halign(Gtk.Align.CENTER)
        btn_box.set_margin_start(20)
        btn_box.set_margin_end(20)
        status_box.append(btn_box)

        self._start_btn = Gtk.Button(label="  Запустить")
        self._start_btn.add_css_class("suggested-action")
        self._start_btn.add_css_class("pill")
        self._start_btn.set_icon_name("media-playback-start-symbolic")
        self._start_btn.connect("clicked", self._on_start)
        btn_box.append(self._start_btn)

        self._stop_btn = Gtk.Button(label="  Остановить")
        self._stop_btn.add_css_class("destructive-action")
        self._stop_btn.add_css_class("pill")
        self._stop_btn.set_icon_name("media-playback-stop-symbolic")
        self._stop_btn.set_visible(False)
        self._stop_btn.connect("clicked", self._on_stop)
        btn_box.append(self._stop_btn)

        # Информация: IP, порт, клиенты, аптайм
        info_grid = Gtk.Grid(column_spacing=24, row_spacing=6)
        info_grid.set_halign(Gtk.Align.CENTER)
        info_grid.set_margin_start(20)
        info_grid.set_margin_end(20)
        info_grid.set_margin_bottom(20)
        status_box.append(info_grid)

        labels = [
            ("Адрес", f"{get_local_ip()}"),
            ("TCP порт", "5555"),
            ("Медиа порт", "5556"),
            ("Клиентов", "0"),
            ("Аптайм", "—"),
        ]
        self._info_values: dict[str, Gtk.Label] = {}
        for row, (key, val) in enumerate(labels):
            lbl_k = Gtk.Label(label=key)
            lbl_k.add_css_class("caption")
            lbl_k.set_halign(Gtk.Align.END)
            lbl_k.set_opacity(0.6)
            info_grid.attach(lbl_k, 0, row, 1, 1)

            lbl_v = Gtk.Label(label=val)
            lbl_v.set_halign(Gtk.Align.START)
            lbl_v.add_css_class("monospace")
            info_grid.attach(lbl_v, 1, row, 1, 1)
            self._info_values[key] = lbl_v

        # ── Настройки ────────────────────────────────────────────────────────
        settings_clamp = Adw.Clamp(maximum_size=480)
        settings_clamp.set_margin_top(12)
        root.append(settings_clamp)

        settings_group = Adw.PreferencesGroup(title="Настройки")
        settings_clamp.set_child(settings_group)

        # Порт
        port_row = Adw.SpinRow.new_with_range(1024, 65535, 1)
        port_row.set_title("TCP порт")
        port_row.set_subtitle("Порт для подключения клиентов")
        port_row.set_value(5555)
        self._port_row = port_row
        settings_group.add(port_row)

        # OTP dev-mode
        otp_row = Adw.SwitchRow()
        otp_row.set_title("Режим разработки (OTP)")
        otp_row.set_subtitle("Показывать код подтверждения в журнале")
        otp_row.set_active(True)
        self._otp_row = otp_row
        # Prefix icon for SwitchRow (compatible with all Adw versions)
        otp_row.add_prefix(Gtk.Image.new_from_icon_name("dialog-password-symbolic"))
        settings_group.add(otp_row)

        # Путь к базе данных
        db_row = Adw.ActionRow()
        db_row.set_title("База данных")
        db_row.set_subtitle(str(DEFAULT_DB))
        db_row.add_prefix(Gtk.Image.new_from_icon_name("drive-harddisk-symbolic"))
        db_row.set_activatable(True)
        db_row.connect("activated", self._on_pick_db)
        db_row.add_suffix(Gtk.Image.new_from_icon_name("document-open-symbolic"))
        self._db_row = db_row
        self._db_path = str(DEFAULT_DB)
        settings_group.add(db_row)

        # ── OTP Монитор ──────────────────────────────────────────────────────
        otp_clamp = Adw.Clamp(maximum_size=480)
        otp_clamp.set_margin_top(12)
        root.append(otp_clamp)

        self._otp_group = Adw.PreferencesGroup(title="Активность авторизации")
        self._otp_group.set_description("Входы и регистрации пользователей")
        otp_clamp.set_child(self._otp_group)

        self._otp_rows: list = []  # список строк Adw.ActionRow

        # ── Журнал ───────────────────────────────────────────────────────────
        log_clamp = Adw.Clamp(maximum_size=480)
        log_clamp.set_margin_top(12)
        root.append(log_clamp)

        log_box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=0)
        log_clamp.set_child(log_box)

        log_header = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=8)
        log_header.set_margin_bottom(6)
        log_box.append(log_header)

        log_title = Gtk.Label(label="Журнал сервера")
        log_title.add_css_class("heading")
        log_title.set_halign(Gtk.Align.START)
        log_title.set_hexpand(True)
        log_header.append(log_title)

        clear_btn = Gtk.Button()
        clear_btn.set_icon_name("edit-clear-symbolic")
        clear_btn.add_css_class("flat")
        clear_btn.set_tooltip_text("Очистить журнал")
        clear_btn.connect("clicked", self._on_clear_log)
        log_header.append(clear_btn)

        log_frame = Gtk.Frame()
        log_frame.add_css_class("card")
        log_box.append(log_frame)

        log_scroll = Gtk.ScrolledWindow()
        log_scroll.set_min_content_height(220)
        log_scroll.set_max_content_height(400)
        log_scroll.set_vexpand(True)
        log_frame.set_child(log_scroll)

        self._log_view = Gtk.TextView()
        self._log_view.set_editable(False)
        self._log_view.set_cursor_visible(False)
        self._log_view.set_wrap_mode(Gtk.WrapMode.WORD_CHAR)
        self._log_view.set_margin_start(10)
        self._log_view.set_margin_end(10)
        self._log_view.set_margin_top(8)
        self._log_view.set_margin_bottom(8)
        self._log_view.add_css_class("monospace")
        log_scroll.set_child(self._log_view)

        self._log_buf = self._log_view.get_buffer()

        # Теги для раскраски
        self._tag_info   = self._log_buf.create_tag("info",   foreground="#4CAF50", weight=700)
        self._tag_warn   = self._log_buf.create_tag("warn",   foreground="#FF9800")
        self._tag_error  = self._log_buf.create_tag("error",  foreground="#F44336", weight=700)
        self._tag_otp    = self._log_buf.create_tag("otp",    foreground="#2196F3", weight=700)
        self._tag_ok     = self._log_buf.create_tag("ok",     foreground="#4CAF50", weight=700)
        self._tag_fail   = self._log_buf.create_tag("fail",   foreground="#F44336", weight=700)
        self._tag_dim    = self._log_buf.create_tag("dim",    foreground="#888888")
        self._tag_client = self._log_buf.create_tag("client", foreground="#9C27B0")

        self._append_log("Сервер готов к запуску.", "dim")

    # ── Вспомогательные методы ───────────────────────────────────────────────
    def _set_running(self, running: bool):
        if running:
            self._status_dot.set_markup('<span foreground="#4CAF50" font="18">●</span>')
            self._status_label.set_text("Работает")
            self._start_btn.set_visible(False)
            self._stop_btn.set_visible(True)
            self._port_row.set_sensitive(False)
            self._otp_row.set_sensitive(False)
        else:
            self._status_dot.set_markup('<span foreground="#888888" font="18">●</span>')
            self._status_label.set_text("Остановлен")
            self._start_btn.set_visible(True)
            self._stop_btn.set_visible(False)
            self._port_row.set_sensitive(True)
            self._otp_row.set_sensitive(True)
            self._info_values["Клиентов"].set_text("0")
            self._info_values["Аптайм"].set_text("—")
            self._clients = 0
            if self._uptime_timer_id:
                GLib.source_remove(self._uptime_timer_id)
                self._uptime_timer_id = None

    def _append_log(self, text: str, tag: str | None = None):
        end = self._log_buf.get_end_iter()
        ts = datetime.now().strftime("%H:%M:%S")
        self._log_buf.insert_with_tags_by_name(end, f"[{ts}] ", "dim")
        end = self._log_buf.get_end_iter()
        if tag:
            self._log_buf.insert_with_tags_by_name(end, text + "\n", tag)
        else:
            self._log_buf.insert(end, text + "\n")
        # Авто-прокрутка вниз
        adj = self._log_view.get_parent().get_vadjustment()
        GLib.idle_add(lambda: adj.set_value(adj.get_upper()) or False)

    # ── OTP монитор ─────────────────────────────────────────────────────────
    def _otp_add_row(self, phone: str, code: str):
        """Добавляет строку ожидания кода в OTP-монитор."""
        ts = datetime.now().strftime("%H:%M:%S")
        row = Adw.ActionRow()
        row.set_title(phone)
        row.set_subtitle(f"Код: {code}  •  Запрошен в {ts}")

        # Иконка статуса — крутящаяся (ожидание)
        spinner = Gtk.Spinner()
        spinner.set_spinning(True)
        spinner.set_size_request(20, 20)
        row.add_suffix(spinner)

        # Кнопка копировать код
        copy_btn = Gtk.Button()
        copy_btn.set_icon_name("edit-copy-symbolic")
        copy_btn.add_css_class("flat")
        copy_btn.set_tooltip_text("Скопировать код")
        copy_btn.set_valign(Gtk.Align.CENTER)
        copy_btn.connect("clicked", lambda _: self.get_clipboard().set(code))
        row.add_suffix(copy_btn)

        self._otp_group.add(row)
        self._otp_rows.append({"phone": phone, "code": code, "row": row, "spinner": spinner})

        # Убираем спиннер через 10 мин (TTL кода)
        GLib.timeout_add_seconds(600, lambda: self._otp_expire(phone) or False)
        return row

    def _otp_update_status(self, phone: str, success: bool, detail: str = ""):
        """Обновляет статус строки OTP после попытки входа."""
        for entry in self._otp_rows:
            if entry["phone"] == phone:
                row: Adw.ActionRow = entry["row"]
                spinner: Gtk.Spinner = entry["spinner"]
                spinner.set_spinning(False)

                if success:
                    icon = Gtk.Image.new_from_icon_name("emblem-ok-symbolic")
                    icon.add_css_class("success")
                    row.set_subtitle(row.get_subtitle() + "  ✓ Вошёл")
                else:
                    icon = Gtk.Image.new_from_icon_name("dialog-error-symbolic")
                    icon.add_css_class("error")
                    row.set_subtitle(row.get_subtitle() + f"  ✗ {detail}")

                # Заменяем спиннер на иконку статуса
                # Удаляем старый спиннер и добавляем иконку через пересборку суффиксов
                spinner.set_visible(False)
                row.add_suffix(icon)
                return

    def _otp_expire(self, phone: str):
        for entry in self._otp_rows:
            if entry["phone"] == phone:
                row: Adw.ActionRow = entry["row"]
                spinner: Gtk.Spinner = entry["spinner"]
                if spinner.get_spinning():
                    spinner.set_spinning(False)
                    row.set_subtitle(row.get_subtitle() + "  (истёк)")

    def _parse_log_line(self, line: str):
        import re
        line = line.strip()
        if not line:
            return

        # [AUTH] LOGIN OK / REGISTER OK / FAIL
        if line.startswith("[AUTH]"):
            if "LOGIN OK" in line or "REGISTER OK" in line:
                m = re.search(r'phone=(\S+).*uid=(\d+)', line)
                action = "Вход" if "LOGIN" in line else "Регистрация"
                if m:
                    self._append_log(f"✅ {action}  {m.group(1)}  (uid={m.group(2)})", "ok")
                    self._otp_update_status(m.group(1), True)
                else:
                    self._append_log(line, "ok")
            elif "FAIL" in line:
                m_phone = re.search(r'phone=(\S+)', line)
                m_reason = re.search(r'reason=(\S+)', line)
                phone = m_phone.group(1) if m_phone else "?"
                reason_map = {
                    "not_found":      "пользователь не найден",
                    "wrong_password": "неверный пароль",
                    "phone_taken":    "телефон уже зарегистрирован",
                    "username_taken": "имя пользователя занято",
                }
                reason = reason_map.get(m_reason.group(1) if m_reason else "", "ошибка")
                self._append_log(f"❌ Неверный вход  {phone}  — {reason}", "fail")
                self._otp_update_status(phone, False, reason)
            else:
                self._append_log(line, "otp")
            return

        if "Client connected" in line:
            self._clients += 1
            self._info_values["Клиентов"].set_text(str(self._clients))
            self._append_log(line, "client")
        elif "Client disconnected" in line:
            self._clients = max(0, self._clients - 1)
            self._info_values["Клиентов"].set_text(str(self._clients))
            self._append_log(line, "dim")
        elif "error" in line.lower() or "fail" in line.lower():
            self._append_log(line, "error")
        elif "beacon" in line.lower() or "mdns" in line.lower() or "listen" in line.lower():
            self._append_log(line, "info")
        else:
            self._append_log(line)

    def _update_uptime(self):
        if self._start_time and self._proc and self._proc.poll() is None:
            delta = datetime.now() - self._start_time
            h, rem = divmod(int(delta.total_seconds()), 3600)
            m, s = divmod(rem, 60)
            self._info_values["Аптайм"].set_text(f"{h:02d}:{m:02d}:{s:02d}")
            return True  # повторять
        return False

    # ── Проверка при старте ──────────────────────────────────────────────────
    def _check_orphan_on_start(self):
        pid = find_orphan_server()
        if pid is None:
            return False
        dlg = Adw.AlertDialog(
            heading="Сервер уже запущен",
            body=f"Найден процесс mase_server (PID {pid}), запущенный вне этого приложения.\n\n"
                 "Завершить его и освободить порты?"
        )
        dlg.add_response("keep", "Оставить")
        dlg.add_response("kill", "Завершить")
        dlg.set_response_appearance("kill", Adw.ResponseAppearance.DESTRUCTIVE)
        dlg.set_default_response("kill")
        dlg.connect("response", self._on_orphan_response, pid)
        dlg.present(self)
        return False

    def _on_orphan_response(self, _dlg, response, pid: int):
        if response == "kill":
            try:
                os.kill(pid, signal.SIGTERM)
                self._append_log(f"Завершён старый процесс mase_server (PID {pid})", "warn")
            except ProcessLookupError:
                pass

    # ── Вспомогательные: проверка портов ────────────────────────────────────
    @staticmethod
    def _pids_on_port(port: int) -> list[int]:
        """Возвращает PID-ы процессов, занимающих TCP-порт."""
        pids = []
        try:
            out = subprocess.check_output(
                ["fuser", f"{port}/tcp"], stderr=subprocess.DEVNULL, text=True
            )
            pids = [int(p) for p in out.split() if p.strip().isdigit()]
        except Exception:
            pass
        return pids

    def _kill_port_squatters(self, port: int) -> bool:
        """Убивает процессы на порту. Возвращает True если порт освободился."""
        pids = self._pids_on_port(port)
        for pid in pids:
            try:
                self._append_log(f"Завершаю процесс PID={pid} на порту {port}", "warn")
                os.kill(pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
        if pids:
            import time; time.sleep(0.8)
        # Проверяем что порт освободился
        return len(self._pids_on_port(port)) == 0

    def _port_is_free(self, port: int) -> bool:
        try:
            s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            s.bind(("0.0.0.0", port))
            s.close()
            return True
        except OSError:
            return False

    # ── Действия ─────────────────────────────────────────────────────────────
    def _on_start(self, _btn):
        if not SERVER_BIN.exists():
            self._show_error(
                "Бинарник не найден",
                f"Файл не найден:\n{SERVER_BIN}\n\nСначала соберите сервер:\n"
                "cmake -S cpp-server -B cpp-server/build && cmake --build cpp-server/build -j4"
            )
            return

        port = int(self._port_row.get_value())

        # Проверяем занятые порты и освобождаем
        for p in (port, port + 1):
            if not self._port_is_free(p):
                self._append_log(f"Порт {p} занят — освобождаю...", "warn")
                if not self._kill_port_squatters(p):
                    self._show_error(
                        f"Порт {p} занят",
                        f"Не удалось освободить порт {p}.\n"
                        "Закройте другое приложение, которое его использует, и попробуйте снова."
                    )
                    return
                self._append_log(f"Порт {p} освобождён.", "info")

        DATA_DIR.mkdir(parents=True, exist_ok=True)
        env = os.environ.copy()
        if self._otp_row.get_active():
            env["MASE_DEV_RETURN_OTP"] = "1"
        env["MASE_NO_MDNS"] = "1"

        try:
            self._proc = subprocess.Popen(
                [str(SERVER_BIN), str(port), self._db_path],
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                text=True,
                bufsize=1,
                env=env,
                cwd=str(SCRIPT_DIR / "cpp-server")
            )
        except Exception as e:
            self._append_log(f"Ошибка запуска: {e}", "error")
            return

        self._start_time = datetime.now()
        self._clients = 0
        self._info_values["TCP порт"].set_text(str(port))
        self._info_values["Медиа порт"].set_text(str(port + 1))
        self._info_values["Адрес"].set_text(get_local_ip())
        self._set_running(True)
        self._append_log(f"Сервер запущен на порту {port}", "info")

        # Таймер аптайма
        self._uptime_timer_id = GLib.timeout_add_seconds(1, self._update_uptime)

        # Читаем вывод в отдельном потоке
        threading.Thread(target=self._read_output, daemon=True).start()

    def _on_stop(self, _btn):
        if self._proc and self._proc.poll() is None:
            self._proc.terminate()
            try:
                self._proc.wait(timeout=3)
            except subprocess.TimeoutExpired:
                self._proc.kill()
        self._proc = None
        self._set_running(False)
        self._append_log("Сервер остановлен.", "warn")

    def _on_clear_log(self, _btn):
        self._log_buf.set_text("")
        self._append_log("Журнал очищен.", "dim")

    def _on_pick_db(self, _row):
        dialog = Gtk.FileDialog()
        dialog.set_title("Выбрать файл базы данных")
        f = Gio.File.new_for_path(self._db_path)
        dialog.set_initial_file(f)

        filter_sqlite = Gtk.FileFilter()
        filter_sqlite.set_name("SQLite (*.sqlite)")
        filter_sqlite.add_pattern("*.sqlite")
        filter_sqlite.add_pattern("*.db")
        filters = Gio.ListStore.new(Gtk.FileFilter)
        filters.append(filter_sqlite)
        dialog.set_filters(filters)

        dialog.save(self, None, self._on_db_picked)

    def _on_db_picked(self, dialog, result):
        try:
            file = dialog.save_finish(result)
            if file:
                self._db_path = file.get_path()
                self._db_row.set_subtitle(self._db_path)
        except Exception:
            pass

    def _read_output(self):
        if not self._proc:
            return
        for line in self._proc.stdout:
            GLib.idle_add(self._parse_log_line, line)
        # Процесс завершился
        GLib.idle_add(self._on_proc_ended)

    def _on_proc_ended(self):
        if self._proc and self._proc.poll() is not None and self._proc.returncode != 0:
            self._append_log(f"Сервер аварийно завершился (код {self._proc.returncode})", "error")
        self._proc = None
        self._set_running(False)
        return False

    def _show_error(self, title: str, body: str):
        dlg = Adw.AlertDialog(heading=title, body=body)
        dlg.add_response("ok", "OK")
        dlg.present(self)

    def do_close_request(self):
        if self._proc and self._proc.poll() is None:
            dlg = Adw.AlertDialog(
                heading="Сервер работает",
                body="Остановить сервер и выйти?"
            )
            dlg.add_response("cancel", "Отмена")
            dlg.add_response("quit", "Остановить и выйти")
            dlg.set_response_appearance("quit", Adw.ResponseAppearance.DESTRUCTIVE)
            dlg.set_default_response("cancel")
            dlg.connect("response", self._on_quit_response)
            dlg.present(self)
            return True  # не закрываем пока
        return False

    def _on_quit_response(self, dlg, response):
        if response == "quit":
            self._on_stop(None)
            self.get_application().quit()


# ── Приложение ───────────────────────────────────────────────────────────────
class MaseServerApp(Adw.Application):
    def __init__(self):
        super().__init__(
            application_id="com.mase.ServerGui",
            flags=Gio.ApplicationFlags.FLAGS_NONE
        )

    def do_activate(self):
        win = MaseServerWindow(self)
        win.present()


def main():
    app = MaseServerApp()
    return app.run(sys.argv)


if __name__ == "__main__":
    sys.exit(main())
