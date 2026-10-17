# gswitch-auto: заметки проекта

Цель: аналог Punto Switcher для Fedora 44 / GNOME 50 / Wayland. Сейчас стоит
gswitch 0.8.0 (ручное исправление по двойному Shift). Возможное продолжение:
форк gswitch с автоматическим режимом (см. gswitch-auto-mode-assessment.md).

## Окружение (на 2026-10-09)

- Fedora 44, GNOME Shell 50.5, сессия Wayland, IBus 1.5.34.
- Раскладки GNOME: us, ru; per-window = false; переключение `<Alt>Shift_L`
  (keybinding gnome-shell, не XKB-опция).
- Пользователь vab состоит в группах wheel, input.

## Что установлено и как

- `gswitch-0.8.0-1.x86_64` из RPM с GitHub Releases (arumata/gswitch, тег
  v0.8.0, sha256 совпал с checksums.txt). Файл RPM сохранён рядом.
- Пользовательский сервис включён: `systemctl --user enable --now gswitch.service`.
  Журнал: `journalctl --user -u gswitch.service -f`.
- Конфиг: `/etc/gswitch/default.conf` (layout-switch=auto, convert-key=0).
  Детект хоткея: `gswitch --detect-layout-switch` → scancodes [56,42], source gnome.
- Трей `gswitch-tray` автозапускается через `/etc/xdg/autostart/gswitch-tray.desktop`.
- Для иконки в панели установлено `gnome-shell-extension-appindicator` (v64,
  поддерживает GNOME 50) и UUID `appindicatorsupport@rgcjonas.gmail.com`
  добавлен в `org.gnome.shell enabled-extensions`. Активируется после перезахода.

## Использование

- Двойной Shift: исправить последнее слово.
- Удерживать один Shift + дважды другой: исправить фразу.
- Повторный двойной Shift сразу после исправления: откат.

## Ключевые выводы исследования (подробно в research/)

- Апстрим автоматику отверг (issues #12, #31 закрыты как not planned);
  разработка в приватном дереве, релизы squash-коммитами → только форк.
- Активную раскладку на GNOME Wayland надёжно даёт IBus: сигнал
  `org.freedesktop.IBus.GlobalEngineChanged` на приватной шине IBus
  (адрес в `~/.config/ibus/bus/*`), `GetGlobalEngine` → `xkb:us::eng`.
- Фокус окна третьим лицам недоступен (`org.gnome.Shell.Introspect` закрыт
  allowlist'ом) → исключения для терминала/паролей только через расширение Shell.
- В демоне нет таблицы evdev-сканкод → XKB-имя (`<AD01>`…), нужно ~48 записей.
- Grab клавиатуры есть в коде, но не используется; без него быстрый набор
  перемешивается с реплеем.
- Детектор языка: взять из punto-rs (MIT): Bloom-словари + буквенные триграммы,
  ~2 МБ, решение за микросекунды. LLM в горячем пути исключена.

## План форка (этапы из отчёта)

0. Прототип: триггер только по Space, словарный фильтр + триграммный порог,
   IBus-трекер, undo двойным Shift, holdoff без grab, минимум 4 буквы,
   клавиша Pause. ~1200-1500 строк с тестами, 1-2 недели.
1. Ежедневное использование: EVIOCGRAB + очередь + ре-инжект, исключения из
   undo, сброс по мыши, чекбокс в трее и локали. +2500-3000 строк, 4-6 недель.
2. Опция: расширение GNOME Shell (фокус, PASSWORD/TERMINAL, per-app). ~300 строк.

Предусловия: Go ≥ 1.25.5 (go.mod), gtk3-devel и libX11-devel для трея.

## Ссылки

- gswitch: https://github.com/arumata/gswitch
- punto-rs (детектор, авто-режим для KDE): https://github.com/rashevskyv/punto-rs
- wordwrap (Python, GNOME Wayland): https://github.com/sgk-dev/wordwrap
