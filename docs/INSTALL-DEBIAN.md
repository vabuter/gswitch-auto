# Установка gswitch-auto на Debian 12 (GNOME 43, X11)

Целевая машина: Debian 12, GNOME Shell 43.9 на Xorg, IBus 1.5.27, раскладки `us`+`ru`,
переключение `<Super>space`. Пакет `.deb` собирается из этого репозитория прямо на машине
(`scripts/build-deb.sh --no-container`) или на Fedora-хосте в podman (`make deb`).

## Вариант 1. Через ИИ-агента

1. Склонировать репозиторий **полностью** (не `--depth 1`: сборке нужен тег `v0.8.0`):

   ```bash
   git clone https://github.com/vabuter/gswitch-auto.git ~/p/gswitch-auto
   ```

2. Запустить агента (например, `claude`) **в терминале внутри сессии GNOME** (не по SSH: агенту
   нужны `DISPLAY` и сессионная шина D-Bus) из каталога `~/p/gswitch-auto` и сказать:

   > Установи gswitch-auto на эту машину по инструкции `docs/AGENT-INSTALL-DEBIAN.md`.

Агент сам проверит машину, скачает Go 1.26 с go.dev в `~/.cache/gswitch-auto-build/` (без root),
соберёт `.deb` с тестами и lintian, проверит демон, расширение и трей и составит отчёт. От вас
понадобится:

- выполнить в своём терминале команды с `sudo`, которые он покажет: `apt install` пакетов для сборки
  и `apt install` самого `.deb` (сам агент `sudo` не запускает);
- нажать Alt+F2, ввести `r`, Enter — перезапуск GNOME Shell, чтобы он увидел расширение (окна и
  терминал при этом остаются);
- набрать текст в проверках (агент сам клавиши не нажимает и не эмулирует).

Запреты для агента (второй демон рядом со службой, тесты с переключением группы XKB на живом
дисплее, правка кода ради прохождения сборки, push) перечислены в начале его инструкции.

## Вариант 2. Вручную

Сборка на самой машине (`B` — каталог для Go и кэшей):

```bash
sudo apt install --no-install-recommends gcc libc6-dev pkg-config libgtk-3-dev libx11-dev libxkbcommon-dev dpkg-dev fakeroot lintian python3 git ca-certificates curl
```

Go 1.26 и nfpm ставятся в `~/.cache/gswitch-auto-build` командами из шагов 3–4
[инструкции для агента](AGENT-INSTALL-DEBIAN.md#3-go-toolchain-no-root), сборка — командой из
шага 5. Результат: `dist/gswitch-auto_<V>-1_amd64.deb` и `dist/checksums-deb.txt`.

Либо собрать на Fedora-хосте и перенести файл (вместо `<debian>` — адрес машины):

```bash
make deb
```

```bash
scp dist/gswitch-auto_*_amd64.deb dist/checksums-deb.txt <debian>:
```

Установка и проверка на Debian:

1. Установить пакет (обычный gswitch, если стоит, будет заменён):

   ```bash
   sudo apt install ./gswitch-auto_<V>-1_amd64.deb
   ```

2. Проверить службу — должно быть `active`:

   ```bash
   systemctl --user is-active gswitch.service
   ```

   Если нет:

   ```bash
   systemctl --user enable --now gswitch.service
   ```

3. Проверить, что демон выбрал источники для X11:

   ```bash
   journalctl --user -u gswitch.service -b | grep 'layout sources'
   ```

   Ожидается `layout sources: x11 (GNOME Shell 43.9); focus sources: extension, x11; strict purpose: true`.

4. Перезапустить оболочку: Alt+F2, `r`, Enter (или выйти и войти). Затем включить расширение фокуса:

   ```bash
   gnome-extensions enable gswitch-focus@vabuter.github.io
   ```

5. Запустить трей (после следующего входа он стартует сам):

   ```bash
   gio launch /usr/share/applications/gswitch-tray.desktop
   ```

6. Включить автопереключение (левый + правый Shift) и в gedit набрать `ghbdtn` и пробел —
   должно получиться `привет`.

Если что-то не так, вывод этих команд показывает, в чём дело:

```bash
gswitch --layout-state --watch 20
```

```bash
gswitch --focus-state --watch 15
```

Полный ручной сценарий — MX1–MX13 в [SPEC-X11.md](SPEC-X11.md) §6.3 (на английском); самые
важные пункты: MX3 (раскладка), MX4 (расширение), MX6 (автоисправление и скорость), MX9 (поля
паролей).

## Что нужно знать про X11

- Демон добавляет в `switch-input-source` клавиши `XF86Launch7` (как upstream) и
  `<Shift>XF86Launch7` (для быстрого переключения при автоисправлении: привязку без модификатора
  GNOME 43 применяет только через 1,5 с). Super+Space продолжает работать.
- **Строгий режим** на X11 включён по умолчанию: исправления идут только в полях, тип которых
  известен, и только когда расширение фокуса активно. Обычные поля пароля GTK3 защищены.
- **Поля пароля в Qt-приложениях и XIM-программы** (xterm, rxvt, st, emacs, Java, Wine)
  сообщают о себе как об обычном тексте; их защищает только список программ-исключений
  (шаблон уже содержит xterm, rxvt, st, emacs, pinentry-qt, ksshaskpass, kleopatra, KeePassXC
  и терминалы).

## Откат

```bash
sudo apt remove gswitch-auto
```

Если до этого стоял обычный gswitch — поставить его `.deb` обратно и включить службу
(`systemctl --user enable --now gswitch.service`). Добавленные клавиши можно убрать через
`gsettings set org.gnome.desktop.wm.keybindings switch-input-source "['<Super>space', 'XF86Keyboard']"`
(или вернуть своё прежнее значение). Каталог сборки: `rm -rf ~/.cache/gswitch-auto-build`.
