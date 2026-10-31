# gswitch-auto: заметки по сопровождению форка

Рабочие заметки оператора: версии, релизы, ребейз на upstream, список изменённых
upstream-файлов, установка и откат. Контракт реализации — `docs/SPEC.md`; здесь
только то, что нужно для сопровождения.

## Что это и почему форк

- gswitch-auto — форк [arumata/gswitch](https://github.com/arumata/gswitch) с
  автоматическим исправлением последнего слова (пара `us`+`ru`). Две целевые машины:
  Fedora 44 / GNOME 50 / Wayland / IBus (RPM) и Debian 12 / GNOME 43.9 / X11 (`.deb`,
  `docs/SPEC-X11.md`). Один репозиторий и одни бинарники: источники раскладки и фокуса
  выбираются при старте демона (`runtime/platform.go`), не build-тегами. Upstream
  автоматику отклонил (issues #12, #31), разрабатывает в приватном
  дереве и выкладывает релизы одиночными squash-коммитами, поэтому форк **жёсткий**:
  он ребейзится на каждый релиз upstream, PR в upstream не планируются.
- Remotes: `origin` = `git@github.com:vabuter/gswitch-auto.git`, `upstream` =
  `https://github.com/arumata/gswitch.git`. Путь модуля Go остаётся
  `github.com/arumata/gswitch` — так диффы при ребейзе минимальны.
- Пакет: RPM `gswitch-auto`, `Provides: gswitch = V-R`, `Obsoletes: gswitch < 0.9`,
  **без** `Conflicts:` (безверсионный `Conflicts: gswitch` совпал бы с собственным
  `Provides` — rpmlint `self-conflict`). Имена бинарников и пути файлов как у upstream,
  поэтому пакет заменяет `gswitch` на месте. Deb `gswitch-auto` (`packaging/nfpm.yaml`,
  override `deb`): `Provides: gswitch (= V-R)`, `Replaces: gswitch`, `Conflicts: gswitch`
  (в Debian Policy 7.4 это допустимо вместе с Provides того же имени).

## Версии

| Сборка | Версия RPM | Пример |
|---|---|---|
| Релиз (тег `v<upstream>+auto.<N>` на чистом HEAD) | `<upstream>+auto.<N>`, Release `1` | `0.8.0+auto.1-1` |
| Коммиты после тега или грязное дерево | `<upstream>+auto.<N>.dev<n>.g<sha>[.dirty]` | `0.8.0+auto.1.dev3.g1a2b3c4` |
| До первого тега на данной базе | `<upstream>+auto.0.dev<n>.g<sha>[.dirty]` | `0.8.0+auto.0.dev64.g1a2b3c4` |

- `<upstream>` — ближайший достижимый тег upstream `vX.Y.Z`; `<n>` — число коммитов
  после тега. Считает `scripts/version.sh` (`make version`); переменная окружения
  `GSWITCH_VERSION` переопределяет вычисление (формат всё равно проверяется).
- Порядок в rpm (проверено на rpm 6.0.2, повторяется `scripts/version.sh --verify`):
  `0.8.0+auto.1-1` > `0.8.0-1`, > `0.8.0-2`, < `0.8.1-1`; dev-сборки после тега N
  выше N и ниже N+1; pre-tag `+auto.0.dev…` выше upstream и ниже `+auto.1`.
- Формы с `~` (`0.8.0~auto.1`) сортируются **ниже** upstream `0.8.0` и запрещены;
  `-` в версии RPM недопустим.
- Нумерация `N` начинается заново на каждой новой базе upstream: первый релиз на
  базе 0.8.1 — `v0.8.1+auto.1`.
- Теги: `v0.8.0+auto.N` (аннотированные). Workflow релиза срабатывает на `tags: ['v*']`
  с условием `contains(github.ref_name, '+auto.')`: в фильтрах GitHub `+` — оператор
  повтора glob, поэтому отбор идёт в `if`.

## Сборка

Зависимости (Fedora): `sudo dnf install golang gcc make pkgconf-pkg-config gtk3-devel
libxkbcommon-devel libX11-devel rpm cpio python3 nodejs`; nfpm:
`go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.48.0` (ставится в `~/go/bin/nfpm`,
этот путь не в PATH — Makefile берёт его по умолчанию через `NFPM`). GoReleaser форком
не используется: `.goreleaser.yml` удалён, его секция nfpms перенесена в
`packaging/nfpm.yaml`.

```bash
make build      # builds/gswitch, builds/gswitch-tray
make test       # go test ./...
make ext-check  # согласованность расширения GNOME Shell (оба дерева)
make rpm        # dist/gswitch-auto-<V>-1.x86_64.rpm + dist/checksums.txt
make install    # make rpm + sudo dnf install
make deb        # dist/gswitch-auto_<V>-1_amd64.deb + dist/checksums-deb.txt (в podman-образе)
make deb-image  # только собрать образ localhost/gswitch-auto-deb:bookworm
```

Deb собирается внутри `docker.io/library/golang:1.26-bookworm` (`packaging/debian12.Containerfile`:
Debian 12 userland + Go 1.26 + nfpm + lintian + Xvfb), потому что в Debian 12 Go 1.19
(backports 1.23), а `go.mod` требует 1.25.5. `scripts/build-deb.sh [--version V]
[--revision R] [--out DIR] [--skip-tests] [--no-container] [--image IMG]` собирает образ и
перезапускает себя в нём: `--userns=keep-id`, `--security-opt label=disable` (не `:Z`, чтобы не
перемаркировать checkout на SELinux-хосте), `HOME=/tmp`, кэши модулей и сборки на именованных
томах `gswitch-auto-gomod`/`gswitch-auto-gocache`; нужна сеть для загрузки модулей. Внутри:
preflight → `version.sh --verify-deb` → `check-extension.sh` → проверка маркеров скриптлетов
→ `go test ./...` → CGO-сборка в `builds/deb/` (никогда поверх `builds/` хоста) → nfpm →
`checksums-deb.txt` → проверки `dpkg-deb` (Depends/Recommends/Provides/Replaces/Conflicts,
права, user unit побайтно = `configs/gswitch.service`, legacy-расширение с `function init(`,
`copyright`, лицензии, нет `/usr/lib/systemd/system/gswitch.service`) → `lintian --fail-on
error --suppress-tags debian-changelog-file-missing`. Версия deb `<V>-<R>` (`0.8.0+auto.1-1`),
порядок проверяет `scripts/version.sh --verify-deb` / `--debcmp A B` (dpkg в контейнере,
python-порт `verrevcmp` на хосте). Никогда не запускать podman-сборку с `builds/` хоста в
качестве цели.

`scripts/build-rpm.sh [--version V] [--release R] [--out DIR] [--skip-tests] [--nfpm PATH]`:
проверка инструментов и всех `src:` из `packaging/nfpm.yaml` (при неполном дереве
падает с перечнем недостающих путей, ничего не собирая) → `version.sh --verify` →
`check-extension.sh` → `go test ./...` → CGO-сборка с `-X main.version=V` → nfpm →
`checksums.txt` → проверки `rpm -qp`: Provides `gswitch = V-R`, Obsoletes
`gswitch < 0.9`, нет Conflicts, лицензия, оба бинарника 0755, unit, udev-правило,
polkit, desktop-файлы, все иконки, `metadata.json` и `extension.js` расширения
(каталог 0755, файлы 0644), лицензии, `hunspell-ru-LICENSE` (%license), README (%doc),
`default.conf` (%config(noreplace)), скриптлеты pretrans/posttrans, нет
`/usr/lib/systemd/system/gswitch.service`, упакованный user unit побайтно совпадает с
`configs/gswitch.service` из тега upstream `v<base>` → проверка пути обновления: если на
хосте стоит upstream `gswitch` (или более старый `gswitch-auto`), новая версия должна
быть выше установленной, граница Obsoletes — покрывать установленный `gswitch`, а
`rpm -U --test` — принять обновление (только читает rpmdb, ничего не ставит, root не
нужен). В CI (пустая rpmdb Ubuntu) этот шаг пропускается.

### Проверка пакета без установки

```bash
R=dist/gswitch-auto-<V>-1.x86_64.rpm
rpm -qpi $R; rpm -qpR $R                       # метаданные, Requires (systemd, gtk3)
rpm -qp --provides $R                          # gswitch = <V>-1, gswitch-auto = <V>-1
rpm -qp --obsoletes $R; rpm -qp --conflicts $R # gswitch < 0.9; пусто
rpm -qplv $R                                   # файлы и права
rpm -qp --scripts $R | grep scriptlet          # pretrans … posttrans
rpm -U --test $R                               # пробное обновление поверх gswitch-0.8.0-1
scripts/version.sh --vercmp <V>-1 0.8.0-1      # 1 = новее (сравнение как в rpm)
```

`rpm -Uvv --test $R 2>&1 | grep Obsoletes` показывает
`Obsoletes: gswitch < 0.9 erases gswitch-0.8.0-1.x86_64`.

## Релиз (чек-лист)

1. `main` зелёный в CI (build/test, lint, RPM-проверки, install-test в fedora:44, deb и
   install-test-deb в debian:12).
2. Локально: `make test`, `make rpm`, `make deb`, ручной сценарий M1–M13 из `docs/SPEC.md`
   §6.4 на хосте (GNOME 50 Wayland) и MX1–MX13 из `docs/SPEC-X11.md` §6.3 на машине с
   Debian 12 (GNOME 43 на Xorg).
3. `git tag -a v0.8.0+auto.N -m "gswitch-auto 0.8.0+auto.N"` на чистом HEAD;
   `scripts/version.sh` должен печатать ровно `0.8.0+auto.N`.
4. `git push origin main v0.8.0+auto.N`. `.github/workflows/release.yml` сверит тег с
   `version.sh`, параллельно соберёт RPM через `scripts/build-rpm.sh` (с тестами и
   проверками) и в образе bookworm `gswitch-auto_<V>-1_amd64.deb` + `checksums-deb.txt`
   (с проверками и lintian). Только если оба пакета собраны и прошли проверки, job
   `publish` одним `gh release create` опубликует релиз со всеми четырьмя файлами и
   инструкциями для Fedora и Debian; при ошибке сборки релиз не создаётся вовсе.
   Секреты не нужны, только автоматический `GITHUB_TOKEN`.
5. Проверить релиз: `sha256sum -c checksums.txt`, `sha256sum -c checksums-deb.txt`,
   установка по разделу ниже.

## Ребейз на новый релиз upstream (чек-лист)

1. `git fetch upstream --tags` и посмотреть, что пришло: `git log --oneline
   v<old>..v<new>`, `git diff --stat v<old> v<new>`.
2. Архивный тег текущего состояния (старые релизы должны остаться достижимыми после
   переписывания истории): `git tag archive/v<old>+auto.<N> main && git push origin
   archive/v<old>+auto.<N>`.
3. `git config rerere.enabled true` (повторные конфликты в тех же местах решаются
   автоматически при следующих ребейзах).
4. `git switch main && git rebase v<new>`. Релизы upstream ложатся линейно на
   `upstream/main`, поэтому `v<new>` — потомок той точки upstream, на которой сидит
   форк, и переигрываются только коммиты форка (`git log v<new>..main`). Если upstream
   переписал историю, указать старую точку явно:
   `git rebase --onto v<new> <старая-точка-upstream> main`. Конфликты ожидаемы только в
   файлах из списка ниже; хуки в `switcher.go` — одиночные строки, логика форка в
   отдельных файлах. Конфликт по удалённому `.goreleaser.yml` означает, что upstream
   поменял упаковку: перенести изменения в `packaging/nfpm.yaml`, файл оставить
   удалённым.
5. Проверить, что upstream не поменял файлы, которые форк не трогает, но на которые
   опирается: `configs/gswitch.service` (пакет кладёт его как есть; проверка
   `build-rpm.sh` сравнивает с тегом `v<new>`), `scripts/{pre,post}{install,remove}.sh`
   (pretrans/posttrans рассчитаны на их поведение, особенно `preremove.sh` с `$1=0`),
   номера строк из `docs/SPEC.md` §4 и `docs/SPEC-X11.md` §4.
   Тела `scripts/preinstall.sh` и `scripts/postinstall.sh` встроены в
   `scripts/deb/preinst.sh` / `postinst.sh` между маркерами `# --- upstream
   scripts/preinstall.sh begin ---` / `end`; если upstream их изменил — пересинхронизировать
   текст между маркерами (`build-deb.sh` падает при расхождении). Два дерева расширения
   (`shell-extension/<uuid>` ESM и `shell-extension/legacy/<uuid>`) должны остаться
   синхронными: общие функции и `IFACE_XML` сравнивает `check-extension.sh`.
6. Поднять границу Obsoletes в `packaging/nfpm.yaml`, если новая база ≥ границы
   (например, база 0.9.0 → `gswitch < 0.10` в `replaces`, в deb-override —
   `gswitch (<< 0.10)` в `replaces` и `conflicts`), и формулировки «0.8.0» в
   README/FORK.md.
   `scripts/version.sh --verify` падает, если база не ниже границы.
7. `make test && make rpm && scripts/version.sh --verify`; версия теперь
   `<new>+auto.0.dev…`.
8. `git push --force-with-lease origin main` (не `--force`: не затереть чужие
   изменения на `origin`).
9. Релиз на новой базе — `v<new>+auto.1` по чек-листу выше.

## Изменённые upstream-файлы

Список по таблице `docs/SPEC.md` §4; здесь ожидаемы конфликты при ребейзе.

| Файл | Что меняет форк |
|---|---|
| `internal/gswitch/runtime/converter.go` | `undoGen`, `ConvertRange(start, end)`, `UndoGeneration()`; `Convert` = scan + `ConvertRange` |
| `internal/gswitch/runtime/switcher.go` | `emitConversionEvents`/`layoutReader()`; поля форка в структуре; однострочные хуки: `initFork`, `layoutSpecs`, `startFork`/`stopFork`, `controlChan`, `loopWait`/`autoTick`, `forkPreProcess`, `OnSpace`, `forkBeforeConversion`, `afterInputDiscarded` |
| `internal/gswitch/runtime/layout.go` | `LayoutInfo.Unmapped` |
| `internal/gswitch/cli.go` | `--auto-check`, `--layout-state [--source]`, `--focus-state` |
| `internal/tray/settings_window.go` | вкладки, Start/Stop, загрузка/применение user-config |
| `internal/tray/tray.go` | пункт меню автомода, бейдж, подсказка |
| `internal/tray/app.go` | клиент демона, `ToggleAuto`, наблюдение за user-config, chmod 0700 |
| `internal/tray/backend_status_notifier.go` | `checkboxTrayBackend` |
| `internal/tray/key_picker.go` | контексты аккордов, passthrough, focus-out, `SuspendHotkeys` |
| `internal/tray/detection_status.go` | `Scancodes []uint16` |
| `internal/tray/settings_language.go` | переименование пункта автомода при смене языка |
| `internal/i18n/i18n.go`, `i18n_test.go`, `locales/en.json`, `locales/ru.json` | политика префикса `strGA` и строки форка |
| `runtime/converter_test.go`, `runtime/layout_test.go`, `tray/key_picker_test.go`, `tray/tray_initial_appearance_test.go` | тесты к изменениям выше (только добавления, кроме подстройки appearance-теста) |
| `.goreleaser.yml` | удалён (заменён `packaging/nfpm.yaml`) |
| `.github/workflows/ci.yml`, `.github/workflows/release.yml` | RPM/deb-сборка и проверки, install-test (fedora:44, debian:12), релиз через `gh` |
| `.gitignore` | `*.rpm` (upstream-RPM в корне для отката не коммитится) |
| `README.md`, `.github/SECURITY.md`, `CONTRIBUTING.md` | документация форка (`CLAUDE.md` — новый файл форка) |

Список сверяется командой `git diff --name-status upstream/main HEAD | grep -v '^A'`
(форк сидит на `upstream/main` = v0.8.0 + 14 коммитов без изменений `.go`).

Намеренно **не** изменяются: `internal/gswitch/runtime/{types.go,input_reader.go,
gnome_wayland_layout_switch.go,deps.go,runner.go}` (и тест `gnome_wayland_layout_switch_test.go`),
`internal/gswitch/config/*`, `.golangci.yml` (исключения из SPEC §3.13 не понадобились:
`userconfig` открывает файлы через `filepath.Clean`, в `ipc/introspect.go` нет длинных
функций, остальное закрыто точечными `//nolint` с причиной),
`scripts/{preinstall,postinstall,preremove,postremove}.sh`, systemd user unit, udev-правило,
polkit-политика, desktop-файлы, `configs/default.conf`, `.github/workflows/{obs,codeql}.yml`,
`.github/dependabot.yml`. Остальное в форке — новые файлы (`packaging/nfpm.yaml`,
`scripts/{build-rpm,version,pretrans,posttrans,check-extension,install-extension}.sh`,
`Makefile`, `shell-extension/`, `internal/gswitch/{fold,lang,layoutstate,focus,userconfig,ipc}`,
новые файлы в `runtime/` и `internal/tray/`, `third_party/punto-rs/`, `docs/`,
`packaging/{debian12.Containerfile,debian/copyright}`, `scripts/{build-deb.sh,deb/}`).

## Установка и откат

Установка поверх upstream (на хосте стоит `gswitch-0.8.0-1`):

```bash
make rpm
sudo dnf install ./dist/gswitch-auto-*.rpm     # Obsoletes удалит gswitch
rpm -q gswitch                                 # "package gswitch is not installed"
gswitch --version                              # gswitch version 0.8.0+auto.N
systemctl --user is-enabled gswitch.service    # enabled
systemctl --user is-active gswitch.service     # active
systemctl --user cat gswitch.service           # как у upstream
```

- При замене upstream-пакета rpm удаляет старый `gswitch` с `$1=0`, и его
  `preremove.sh` выключает unit у всех вошедших пользователей. `scripts/pretrans.sh`
  заранее записывает `/run/gswitch-auto/enabled.<uid>` для тех, у кого unit был
  включён, `scripts/posttrans.sh` после транзакции снова делает `enable --now` там,
  где unit оказался выключен, удаляет маркеры и печатает подсказку про расширение.
  Если что-то пошло не так: `systemctl --user enable --now gswitch.service`.
- Расширение `gswitch-focus@vabuter.github.io`: GNOME Shell видит новые и обновлённые
  расширения только после перелогина (Wayland). Выйти и войти, затем включить в
  настройках трея (вкладка автоисправления) или `gnome-extensions enable
  gswitch-focus@vabuter.github.io`. Перезапустить трей сразу:
  `pkill -x gswitch-tray; gio launch /usr/share/applications/gswitch-tray.desktop`.

Откат на upstream (RPM лежит в корне репозитория, sha256 сверен с `checksums.txt`
релиза):

```bash
sudo dnf swap gswitch-auto ./gswitch_0.8.0_linux_amd64.rpm
systemctl --user enable --now gswitch.service  # preremove.sh при удалении выключил unit
gnome-extensions disable gswitch-focus@vabuter.github.io   # по желанию; файлы удалены пакетом
```

Файлы `~/.config/gswitch/{auto.conf,exceptions-words.txt,exceptions-apps.txt}` после
отката остаются; upstream их не читает (`ui.conf` по-прежнему общий).

### Debian 12 (GNOME 43 на Xorg)

```bash
make deb                                            # на Fedora-хосте, через podman
scp dist/gswitch-auto_*_amd64.deb debian:           # перенести на машину
sudo apt install ./gswitch-auto_<V>-1_amd64.deb     # заменит upstream gswitch (.deb)
dpkg -s gswitch                                     # not installed / config-files
gswitch --version                                   # gswitch version <V>
systemctl --user is-enabled gswitch.service         # enabled (иначе: enable --now)
journalctl --user -u gswitch.service -b | grep 'layout sources'
#   layout sources: x11 (GNOME Shell 43.9); focus sources: extension, x11; strict purpose: true
```

- Порядок замены (Conflicts+Replaces+Provides) зависит от фронтенда, но upstream
  `preremove.sh` в любом случае выключает и останавливает user unit **до** нашего `preinst`:
  при `dpkg -i` в том же прогоне сначала выполняется `prerm remove in-favour` upstream-пакета
  (`dpkg-query` показывает его `half-configured`/`half-installed`), при `apt install` apt
  удаляет `gswitch` отдельным прогоном dpkg (обычный `prerm remove`), и наш `preinst` видит
  upstream в состоянии **`config-files`**. `scripts/deb/preinst.sh` при `$1 = install` и
  любом статусе `gswitch`, кроме пустого и `not-installed` (в том числе `config-files`),
  пишет маркер `/run/gswitch-auto/replacing.<uid>` для вошедших пользователей,
  `postinst.sh` по маркерам делает `daemon-reload` и `enable --now`, если unit выключен.
  Эвристика «upstream стоит или стоял»: unit включится и у того, кто выключил его
  намеренно, и после давнего `apt remove gswitch` без purge (остались `config-files`).
  Отменить: `sudo apt purge gswitch` заранее или `systemctl --user disable --now
  gswitch.service` после. Обычные обновления `gswitch-auto` (`$1 = upgrade`) не
  затрагиваются.
- Расширение: пакет ставит **legacy**-дерево (`function init()`, `shell-version 43/44`).
  GNOME 43 сканирует каталоги расширений один раз за сессию, `ReloadExtension` не
  поддерживается: выйти и войти, либо Alt+F2, `r`, Enter (только X11). Затем включить в
  трее или `gnome-extensions enable gswitch-focus@vabuter.github.io`. Пока расширение не
  включено, в строгом режиме (по умолчанию на X11) автоисправление недоступно
  (`AutoUnavailableReason = focus-extension-required`), трей показывает подсказку.
- `gswitch --detect-layout-switch` на Debian даёт `scancodes: [186]` (`XF86Launch7`), демон
  добавляет его в `switch-input-source` (поведение upstream для GNOME X11); Super+Space
  продолжает переключать раскладку. Для автозамены демон форка добавляет туда же
  `<Shift>XF86Launch7` (лог `Added <Shift>XF86Launch7 …`) и переключает Shift+F16: привязку
  без модификатора GNOME применяет только через 1,5 с после отпускания клавиши, а всплывающее
  окно переключателя в это время глотает ввод (SPEC-X11 F21). Привязка постоянная, как и
  upstream-овская; после удаления пакета её можно убрать через `gsettings set
  org.gnome.desktop.wm.keybindings switch-input-source …`.
- Диагностика: `gswitch --layout-state --watch 20` (`Session: x11`, `GNOME Shell: 43.9`,
  таблица `us,ru`, группа и индекс; переключения Super+Space видны строками с задержкой
  ≤ 50 мс), `gswitch --focus-state --watch 15`, `busctl --user get-property
  io.github.vabuter.GswitchAuto /io/github/vabuter/GswitchAuto io.github.vabuter.GswitchAuto
  FocusSuppressedReason` (`excluded-app` в xterm — XIM-клиент сообщает `normal`, F22;
  `purpose-unknown` в GTK3-поле пароля — это норма, F8).
- Откат: `sudo apt install ./gswitch_0.8.0_linux_amd64.deb` (если deb upstream сохранён) или
  `sudo apt remove gswitch-auto`, затем `systemctl --user enable --now gswitch.service`.
- Интеграционные тесты X11 (только чтение): `GSWITCH_X11_TEST=1 go test
  ./internal/gswitch/layoutstate/ ./internal/gswitch/focus/ -run X11 -v` против `$DISPLAY`
  (на хосте — Xwayland `:0`). Вариант с переключением группы (`GSWITCH_X11_TEST_SWITCH=1`)
  **только** в контейнере с Xvfb (рецепт в `docs/SPEC-X11.md` §6.2), никогда на живом
  Xwayland хоста.

## Ограничения, о которых стоит помнить

- Демон работает под неизменённым upstream unit (`ProtectHome=read-only`) и ничего не
  пишет в `$HOME`. Переключение автомода аккордом/через D-Bus и выученные слова
  сохраняет трей (через `AutoEnabledUnsaved`/`PendingLearnedWords`); без запущенного
  трея они живут до перезапуска демона. Вариант с `ConfigurationDirectory=` в unit
  отклонён (меняет unit, зафиксированный решением D2).
- `gswitch --debug` рядом с запущенной службой: работают оба демона; отладочный
  экземпляр пишет «IPC disabled», но всё равно исправляет текст — перед отладкой
  остановить службу (`systemctl --user stop gswitch.service`).
- `shell-version` расширения закреплён на `["50"]` (ESM) и `["43", "44"]` (legacy): при
  переходе Fedora на GNOME 51 расширение станет OUT_OF_DATE — поднять версию в
  `metadata.json`. Пакет ставит ровно одно дерево; RPM на GNOME 43 (или deb на GNOME 50)
  даст OUT_OF_DATE.
- X11: источник раскладки — опрос `XkbGetState` каждые 50 мс (без `XkbSelectEvents`:
  у jezek/xgb нет XKB-пакета, а регистрация событий идёт через глобальную незащищённую
  карту). Изменение группы видно с задержкой до 50 мс; опоздавшее изменение может только
  пропустить слово, но не исправить его в неверной раскладке. Если `<Shift>XF86Launch7`
  добавить не удалось (предупреждение в логе) или переключатель раскладки — другая одиночная
  клавиша, автозамена ждёт всплывающее окно GNOME (~1,5 с), а набранное в это время
  теряется, как и при ручной конвертации upstream.
- X11, строгий режим (`auto-strict-purpose=auto` ⇒ true на X11 и при неизвестном типе
  сессии): без расширения автоисправление недоступно; приложения, которые вовсе не
  фокусируют IBus, никогда не исправляются. Но строгий режим **не защищает** XIM-клиентов
  (xterm, urxvt, st, Emacs, Java, Wine: IBus отдаёт панели тип по умолчанию FREE_FORM, и
  поле выглядит как `normal`) и приложения Qt (Qt не сообщает IBus о переходе в поле
  пароля): их пароли исправляются, если приложения нет в списке исключений. Шаблон
  исключений содержит XIM-терминалы, Emacs, `sun-awt-x11-*`, `*.exe` и Qt-запросы паролей
  (K-X4, F22). При выключенном строгом
  режиме GTK3-поле пароля на X11 выглядит как «нет поля» (ibus-im-module не делает focus-in
  для `visibility=FALSE`) и **исправляется** — защищает только список исключений
  (менеджеры паролей в шаблоне). Документированная дыра K-X5, не дефект.
- X11 без `XDG_SESSION_TYPE` в окружении `systemd --user` (не GDM, свой `.xsession`): демон
  не выбирает X11-источники (`layout-source-unsupported`) и работает строго — fail-closed.
  Лечение: `systemctl --user import-environment DISPLAY XAUTHORITY XDG_SESSION_TYPE`.
- `layout-source=ibus` при GNOME Shell < 49.3 не бесполезен, а вреден: движок там всегда
  `xkb:us::eng`, трекер навсегда подтверждает первую раскладку, и правильное слово во
  второй (клавиши `ghbdtn`, на экране `привет`) считается набранным не в той раскладке;
  замена дважды переключает раскладку и прерывается. Поэтому в X11-сессии ниже 49.3 демон
  его отклоняет (`selectPlatform`: предупреждение `layout-source=ibus is refused in an X11
  session …`, `AutoUnavailableReason = layout-source-unsupported`; нужен `auto` или `x11`),
  а на Wayland оставляет как явное переопределение с предупреждением о вреде.
- Deb: повторное включение user unit после замены upstream работает по эвристике
  «upstream стоит или стоял» (включая его `config-files`, см. раздел установки): может
  включить намеренно выключенный unit. Не вопрос безопасности: остановленный демон ничего
  не печатает, автомод по умолчанию выключен.

## Решения, принятые при реализации

- `auto-min-letters` по умолчанию **3** (паритет с punto-rs, SPEC Q3): Go-порт проходит
  eval-гейт с `DefaultOptions` — 0 ложных исправлений на обоих сплитах; recall test:
  ru_wrong 102/105, en_wrong 95/100, двухбуквенные 14/26; dev: 101/105, 94/101, 13/24
  (`go test ./internal/gswitch/lang -run TestEvalGates -v`).
- Вето `edge-shown-known` (SPEC C29): слово с конечной пунктуацией, чьё показанное ядро —
  словарное английское слово со средней стоимостью триграмм ≤ 6.0 бит/букву, не
  исправляется (`it.`, `bye.`); цена — редкие русские слова вроде `ct.`→`сею`.
- `auto-delay-ms=3`, `auto-switch-delay-ms=60`, `auto-holdoff-ms=120` — значения из SPEC;
  на GNOME 50 **не измерялись** (нужен ручной прогон M6 на хосте; агенты не нажимают
  клавиши). Если после замены первые буквы набираются в старой раскладке — поднять
  `auto-switch-delay-ms`; если буквы теряются или переставляются — `auto-delay-ms`.
- M9 (момент срабатывания `<Alt>Shift_L` при RAlt-first в аккорде выделения) не
  прогонялся; демон в любом случае восстанавливает раскладку, если она переключилась
  во время аккорда (SPEC C14), а README советует нажимать Shift первым.
- `Obsoletes: gswitch < 0.9` при `Provides: gswitch = V-R` (решение D7): диапазон
  Obsoletes перекрывает собственный Provides, поэтому rpmlint, вероятно, выдаст
  предупреждение `self-obsoletion` (rpmlint на хосте нет). rpm и dnf сопоставляют
  Obsoletes с **именами** пакетов, так что на установку это не влияет (`rpm -U --test`
  стирает только `gswitch-0.8.0-1`). Если предупреждение станет проблемой — вариант
  Fedora для переименований `Obsoletes: gswitch < V-R`, но это изменение D7.
