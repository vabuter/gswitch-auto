# Установка gswitch-auto на Debian 12 (GNOME 43, X11)

Целевая машина: Debian 12, GNOME Shell 43.9 на Xorg, IBus 1.5.27, раскладки `us`+`ru`,
переключение `<Super>space`. Пакет `.deb` собирается из этого репозитория прямо на машине
(`scripts/build-deb.sh --no-container`) или на Fedora-хосте в podman (`make deb`).

Работать нужно в сеансе **«GNOME на Xorg»** (выбирается шестерёнкой на экране входа): в сеансе
Wayland с GNOME 43 автоисправление не работает. Проверка: `echo $XDG_SESSION_TYPE` должна
показать `x11`.

## Вариант 1. Через ИИ-агента

1. На Debian 12 с GNOME git по умолчанию не установлен:

   ```bash
   sudo apt install git
   ```

   (если sudo не настроен — `su -c 'apt install git'` с паролем root.)

   Склонировать репозиторий **полностью** (не `--depth 1`: сборке нужен тег `v0.8.0`):

   ```bash
   git clone https://github.com/vabuter/gswitch-auto.git ~/p/gswitch-auto
   ```

2. Запустить агента (например, `claude`) **в терминале внутри сеанса GNOME** (не по SSH: агенту
   нужны `DISPLAY` и сессионная шина D-Bus) из каталога `~/p/gswitch-auto` и сказать:

   > Установи gswitch-auto на эту машину по инструкции `docs/AGENT-INSTALL-DEBIAN.md`.

Агент сам проверит машину, скачает Go 1.26 с go.dev в `~/.cache/gswitch-auto-build/` (без root),
соберёт `.deb` с тестами и lintian (5–15 минут), проверит демон, расширение и трей и составит
отчёт. От вас понадобится:

- выполнять команды с `sudo`, которые он покажет: `apt update`, `apt install` пакетов для сборки
  (возможно, с `gjs`, `ibus-gtk3`, `ibus-gtk4`, если их нет) и `apt install` самого `.deb`; для
  проверок может понадобиться `gedit` (сам агент `sudo` не запускает). Выполняйте их **в отдельном окне или вкладке
  терминала**, а не через `!` в сеансе агента: там sudo не сможет спросить пароль. Если sudo не
  настроен (при установке Debian задан пароль root), выполняйте эти команды как `su -c '…'`;
- нажать Alt+F2, ввести `r`, Enter — перезапуск GNOME Shell, чтобы он увидел расширения (окна и
  терминал при этом остаются);
- разрешить или нет включение расширения AppIndicator (значок трея; оно показывает значки всех
  программ, не только gswitch);
- набирать текст в проверках (агент сам клавиши не нажимает и не эмулирует).

Если сеанс агента прервался (или пришлось выйти из сеанса GNOME), продолжить можно командой
`cd ~/p/gswitch-auto && claude --continue`: агент ведёт журнал в
`~/.cache/gswitch-auto-build/install-log.md` и продолжит с последнего выполненного шага.

Запреты для агента (второй демон рядом со службой, запуск тестов вне сборки и с переключением
группы XKB на живом дисплее, правка кода ради прохождения сборки, push) перечислены в начале его
инструкции.

## Вариант 2. Вручную

Если sudo не настроен, команды с `sudo` ниже выполняйте как `su -c '…'`.

Сборка на самой машине. Пакеты для сборки:

```bash
sudo apt update
```

```bash
sudo apt install --no-install-recommends gcc libc6-dev pkg-config libgtk-3-dev libx11-dev libxkbcommon-dev dpkg-dev fakeroot lintian python3 git ca-certificates
```

Go 1.26, nfpm и модули Go скачиваются в `~/.cache/gswitch-auto-build` командами из шагов 3–4
[инструкции для агента](AGENT-INSTALL-DEBIAN.md#3-go-toolchain-no-root-minutes) (сетевую ошибку,
например таймаут TLS, лечит повторный запуск той же команды), сборка — командой из шага 5 (сеть ей
уже не нужна; выполнять из любого каталога: команды сами переходят в `~/p/gswitch-auto`). Результат:
`~/p/gswitch-auto/dist/gswitch-auto_<V>-1_amd64.deb` и `dist/checksums-deb.txt` (в
`checksums-deb.txt` — имя последнего собранного пакета).

Либо собрать на Fedora-хосте и перенести файл (вместо `<debian>` — адрес машины):

```bash
make deb
```

```bash
scp dist/"$(awk '{print $2}' dist/checksums-deb.txt)" dist/checksums-deb.txt <debian>:
```

и на Debian проверить контрольную сумму (из домашнего каталога; ожидается `OK`, в русской локали
`ЦЕЛ`):

```bash
sha256sum -c checksums-deb.txt
```

Установка и проверка на Debian:

1. Установить пакет (обычный gswitch, если стоит, будет заменён). Сообщение apt вида
   «… _apt … pkgAcquire::Run (13: …)» о скачивании без песочницы безвредно. После сборки на самой
   машине:

   ```bash
   cd ~/p/gswitch-auto && sudo apt install ./dist/"$(awk '{print $2}' dist/checksums-deb.txt)"
   ```

   Без sudo (здесь нельзя просто обернуть команду в `su -c '…'`: мешают одинарные кавычки awk):

   ```bash
   cd ~/p/gswitch-auto && su -c "apt install $PWD/dist/$(awk '{print $2}' dist/checksums-deb.txt)"
   ```

   После `scp` — из домашнего каталога:

   ```bash
   sudo apt install ~/gswitch-auto_<V>-1_amd64.deb
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

   и расширение для значка в трее (ставится вместе с пакетом `gnome-shell-extension-appindicator`;
   без него значок трея не появится):

   ```bash
   gnome-extensions enable ubuntu-appindicators@ubuntu.com
   ```

   Состояние смотрится через `gnome-extensions info <uuid>`: у работающего расширения GNOME 43
   показывает `ENABLED`. Если `gnome-extensions` молча завершается с ошибкой, не хватает пакета
   `gjs` (`sudo apt install gjs`).

5. Запустить трей (после следующего входа он стартует сам):

   ```bash
   gio launch /usr/share/applications/gswitch-tray.desktop
   ```

6. Включить автоисправление (левый Shift + правый Shift) и в gedit (`sudo apt install gedit`, если
   его нет) или в «Текстовом редакторе» (gnome-text-editor) набрать `ghbdtn` и пробел — должно
   получиться `привет`. Если исправлений нет нигде, проверить, что стоят модули IBus для
   GTK (`sudo apt install ibus-gtk3 ibus-gtk4`, затем перезапустить программы).

Если что-то не так, вывод этих команд покажет, в чём дело:

```bash
gswitch --layout-state --watch 20
```

```bash
gswitch --focus-state --watch 15
```

Полный ручной сценарий — MX1–MX13 в [SPEC-X11.md](SPEC-X11.md) §6.3 (на английском); самые
важные пункты: MX3 (раскладка), MX4 (расширение), MX6 (автоисправление и скорость), MX9 (поля
паролей). Отмена исправления двойным Shift запоминает слово как исключение (настройка по
умолчанию); убрать его можно в настройках трея: «Автоисправление» → «Слова-исключения».

## Что нужно знать про X11

- Демон добавляет в `switch-input-source` клавиши `XF86Launch7` (как upstream) и
  `<Shift>XF86Launch7` (для быстрого переключения при автоисправлении: привязку без модификатора
  GNOME 43 применяет только через 1,5 с). Super+Space продолжает работать.
- **Строгий режим** на X11 включён по умолчанию: исправления идут только в полях, тип которых
  известен, и только когда расширение фокуса активно. Обычные поля пароля GTK3 защищены.
- **Поля пароля в Qt-приложениях и XIM-программы** (xterm, rxvt, st, emacs, Java, Wine)
  сообщают о себе как об обычном тексте; их защищает только список программ-исключений
  (стандартный список исключений уже содержит xterm, rxvt, st, emacs, pinentry-qt, ksshaskpass,
  kleopatra, KeePassXC и терминалы).

## Откат

```bash
sudo apt remove gswitch-auto
```

```bash
pkill -x gswitch-tray
```

`apt purge gswitch-auto` удалит ещё `/etc/gswitch` и файлы автозапуска в `/etc/xdg/autostart/`.
Если до этого стоял обычный gswitch и его `.deb` сохранён — поставить его обратно и включить
службу (`systemctl --user enable --now gswitch.service`). Расширения выключаются через
`gnome-extensions disable gswitch-focus@vabuter.github.io` (и
`gnome-extensions disable ubuntu-appindicators@ubuntu.com`, если оно было включено только ради
трея). Добавленные клавиши можно убрать через
`gsettings set org.gnome.desktop.wm.keybindings switch-input-source "['<Super>space', 'XF86Keyboard']"`
(или вернуть своё прежнее значение). Удалить каталог сборки (кэш модулей Go доступен только для
чтения, поэтому сначала `chmod`):

```bash
chmod -R u+w ~/.cache/gswitch-auto-build && rm -rf ~/.cache/gswitch-auto-build
```

Пакеты для сборки удаляйте только те, которых не было до установки (`apt-get -s purge …` сначала
покажет, что именно будет удалено); голый `sudo apt autoremove` может снести и посторонние пакеты.
