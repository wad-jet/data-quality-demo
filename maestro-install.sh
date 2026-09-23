#!/usr/bin/env bash
#
# maestro-install.sh — bootstrap проекта для maestro через agpack.
#
# Готовит базу для разработки с помощью maestro в пустом каталоге (новый проект)
# или в каталоге существующего проекта, где maestro ранее не применялся:
#   1. Проверяет предусловия (git, python3 >= 3.11).
#   2. Устанавливает/настраивает agpack (uv tool или pipx, с нюансами по OS).
#   3. Создаёт agpack.yml (идемпотентно, не перезаписывает существующий).
#   4. Запускает `agpack sync` — разворачивает skills/commands/agents в .opencode/.
#   5. Подключает плагин opencode `maestro-bootstrap` (мерж, без перезаписи).
#   6. Загружает `maestro-update.sh` (для будущих обновлений maestro).
#   7. Выдаёт краткую инструкцию по запуску инициализации в opencode.
#
# Совместимость: bash 3.2+ (macOS GNU bash 3.2.57); bash-на-macOS/Linux.
# Windows — через WSL/Git Bash (см. оговорку в доке).
#
# Флаги:
#   --global   регистрировать плагин в глобальном конфиге opencode
#              (по умолчанию — в .opencode/opencode.json проекта)
#   --help     краткая справка и выход
#
# Содержимое agpack.yml скачивается из канона maestro-install/agpack.yml в репозитории
# (тот же источник, что читает maestro-update.sh при merge-add новых компонентов).
set -euo pipefail

# --- Константы -------------------------------------------------------------

PLUGIN_SPEC="maestro-bootstrap@git+https://github.com/wad-jet/maestro.git"
REPO_URL="https://github.com/wad-jet/maestro"
RAW_URL="https://raw.githubusercontent.com/wad-jet/maestro/main/maestro-install.sh"
MAESTRO_UPDATE_RAW_URL="https://raw.githubusercontent.com/wad-jet/maestro/main/maestro-update.sh"
AGPACK_YML_RAW_URL="https://raw.githubusercontent.com/wad-jet/maestro/main/maestro-install/agpack.yml"

# fetch <url> <dest> — скачивание через curl (приоритет) или wget.
# Возвращает 0 при успехе, ненулевой код при неудаче.
fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$2" "$1"
  else
    return 1
  fi
}

# --- Вспомогательные функции ----------------------------------------------

say()  { printf '%s\n' "$*"; }
info() { printf '\033[1;34m[maestro-install]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[maestro-install] ВНИМАНИЕ:\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m[maestro-install] ОШИБКА:\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'USAGE'
maestro-install — подготовка проекта для maestro через agpack.

Использование:
  bash maestro-install.sh [--global] [--help]

Флаги:
  --global   зарегистрировать плагин maestro-bootstrap в глобальном конфиге
             opencode (~/.config/opencode/opencode.json).
             По умолчанию — в .opencode/opencode.json проекта.
  --help     показать эту справку и выйти.

Предусловия: bash (macOS/Linux), git, python3 >= 3.11.
Windows: запускайте через WSL или Git Bash.
USAGE
}

# --- Разбор аргументов ----------------------------------------------------

GLOBAL_MODE=0
for arg in "$@"; do
  case "$arg" in
    --help|-h)   usage; exit 0 ;;
    --global)    GLOBAL_MODE=1 ;;
    --*)         die "неизвестный флаг: $arg (см. --help)" ;;
    *)           die "неожиданный аргумент: $arg (см. --help)" ;;
  esac
done

# --- 1. Preflight -----------------------------------------------------------

command -v git >/dev/null 2>&1 \
  || die "не найден 'git'. Установите git и повторите."

if ! command -v python3 >/dev/null 2>&1; then
  die "не найден 'python3'. Установите Python >= 3.11 и повторите."
fi

PY_MAJOR=$(python3 -c 'import sys; print(sys.version_info[0])')
PY_MINOR=$(python3 -c 'import sys; print(sys.version_info[1])')
if [ "$PY_MAJOR" -lt 3 ] || { [ "$PY_MAJOR" -eq 3 ] && [ "$PY_MINOR" -lt 11 ]; }; then
  die "требуется Python >= 3.11 (найдено $(python3 --version 2>&1)). Обновите Python и повторите."
fi

# --- 2. Обнаружение/установка agpack ---------------------------------------

AGPACK=""
if command -v agpack >/dev/null 2>&1; then
  AGPACK="$(command -v agpack)"
elif [ -x "$HOME/.local/bin/agpack" ]; then
  AGPACK="$HOME/.local/bin/agpack"
fi

if [ -z "$AGPACK" ]; then
  info "agpack не найден — устанавливаю."

  if command -v uv >/dev/null 2>&1; then
    info "установка через 'uv tool install agpack'..."
    uv tool install agpack
  elif command -v pipx >/dev/null 2>&1; then
    info "установка через 'pipx install agpack'..."
    pipx install agpack
  else
    cat <<'EOT'
[maestro-install] ОШИБКА: не найдены ни 'uv', ни 'pipx'.

Установите agpack одним из способов:
  # предпочтительно (uv)
  uv tool install agpack

  # или (pipx)
  pipx install agpack

Примечания:
  - macOS (pipx): после установки agpack может не находиться в новых консолях.
    Выполните:  pipx ensurepath   (или: export PATH="$HOME/.local/bin:$PATH")
  - Ubuntu (pipx): нужен python3-venv:  sudo apt install python3-venv python3-pip

После установки запустите этот скрипт заново.
EOT
    exit 1
  fi

  if [ -x "$HOME/.local/bin/agpack" ]; then
    AGPACK="$HOME/.local/bin/agpack"
  elif command -v agpack >/dev/null 2>&1; then
    AGPACK="$(command -v agpack)"
  else
    die "agpack установлен, но не найден в PATH. Выполните 'pipx ensurepath' и запустите скрипт заново."
  fi
fi

info "agpack: $AGPACK"

# --- 3. Создание agpack.yml (идемпотентно, скачивание канона) ----------------

if [ -f "agpack.yml" ]; then
  info "agpack.yml уже существует — пропускаю создание (не перезаписываю)."
else
  info "скачиваю agpack.yml из канона ($AGPACK_YML_RAW_URL)..."
  if fetch "$AGPACK_YML_RAW_URL" "agpack.yml.tmp"; then
    mv "agpack.yml.tmp" "agpack.yml"
    info "agpack.yml создан из канона maestro-install/agpack.yml."
  else
    rm -f "agpack.yml.tmp"
    die "не удалось скачать agpack.yml ($AGPACK_YML_RAW_URL). Требуются curl или wget и сеть; скачайте файл вручную и повторите."
  fi
fi

# --- 3a. Миграция agpack.yml (rename skills/maestro-init|maestro-new -> skills/maestro-setup) ---
if [ -f "agpack.yml" ]; then
  python3 - <<'PY'
path = "agpack.yml"
with open(path, "r", encoding="utf-8") as f:
    text = f.read()
target = '      path: skills/maestro-setup'
changed = False
for old in ('      path: skills/maestro-init', '      path: skills/maestro-new'):
    if old in text:
        text = text.replace(old, target)
        changed = True
if changed:
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)
    print("maestro-install: agpack.yml: skills/maestro-init|maestro-new -> skills/maestro-setup")
PY
fi

# --- 4. agpack sync ----------------------------------------------------------

info "запускаю 'agpack sync'..."
"$AGPACK" sync

# --- 4a. Очистка stale-артефактов (agpack не прунит) ---
rm -rf .opencode/commands/maestro.md .opencode/skills/maestro-init .opencode/commands/maestro-new.md .opencode/skills/maestro-new

# --- 5. Регистрация плагина maestro-bootstrap (идемпотентно) ------------------

if [ "$GLOBAL_MODE" -eq 1 ]; then
  : "${XDG_CONFIG_HOME:=$HOME/.config}"
  CONFIG_FILE="$XDG_CONFIG_HOME/opencode/opencode.json"
else
  CONFIG_FILE=".opencode/opencode.json"
fi

info "регистрирую плагин в конфиге opencode: $CONFIG_FILE"
mkdir -p "$(dirname "$CONFIG_FILE")"

python3 - "$CONFIG_FILE" "$PLUGIN_SPEC" <<'PY'
import json
import os
import sys

config_path, plugin_spec = sys.argv[1], sys.argv[2]

data = {}
if os.path.exists(config_path):
    try:
        with open(config_path, "r", encoding="utf-8") as f:
            content = f.read().strip()
            if content:
                data = json.loads(content)
    except (json.JSONDecodeError, OSError) as e:
        sys.stderr.write("maestro-install: не могу прочитать %s: %s\n" % (config_path, e))
        sys.exit(1)

plugins = data.get("plugin")
if plugins is None:
    plugins = []
if not isinstance(plugins, list):
    sys.stderr.write("maestro-install: ключ 'plugin' в %s — не массив; не трогаю.\n" % config_path)
    sys.exit(1)

if plugin_spec in plugins:
    print("maestro-install: плагин уже подключён (%s)" % plugin_spec)
else:
    plugins.append(plugin_spec)
    data["plugin"] = plugins
    with open(config_path, "w", encoding="utf-8") as f:
        json.dump(data, f, ensure_ascii=False, indent=2)
        f.write("\n")
    print("maestro-install: плагин добавлен в конфиг opencode")
PY

# --- 5a. Memory layer (опционально) ------------------------------------------

if [[ -t 0 ]]; then
  read -r -p "Подключить memory layer (опциональная векторная память сессий)? (y/N) " memory_yn
  if [[ "${memory_yn,,}" == "y" ]]; then
    if [[ -n "${XDG_DATA_HOME:-}" ]]; then
      memory_data_dir="$XDG_DATA_HOME/maestro"
    elif [[ "$(uname)" == "Darwin" ]]; then
      memory_data_dir="$HOME/Library/Application Support/maestro"
    else
      memory_data_dir="$HOME/.local/share/maestro"
    fi
    mkdir -p "$memory_data_dir/memory"
    touch "$memory_data_dir/memory/enabled.flag"
    if command -v npm >/dev/null 2>&1 || command -v bun >/dev/null 2>&1; then
      info "memory layer: маркер установлен ($memory_data_dir/memory/enabled.flag)."
      echo "  Зависимости установит плагин при первом запуске (см. manual_docs/how-to/enable-memory.md)."
    else
      warn "memory layer выбран, но npm/bun не найден в PATH — зависимости установить нечем."
      echo "  Установите Node.js/npm и повторите, либо включите память позже через maestro-assistant."
    fi
  fi
fi

# --- 6. Загрузка maestro-update.sh (идемпотентно, всегда перезаписывает) -----

if fetch "$MAESTRO_UPDATE_RAW_URL" "maestro-update.sh"; then
  info "maestro-update.sh загружен."
elif command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1; then
  warn "не удалось загрузить maestro-update.sh: продолжаю без него — скачайте вручную: $MAESTRO_UPDATE_RAW_URL"
else
  warn "не найден 'curl'/'wget' — maestro-update.sh не загружен. Скачайте вручную: $MAESTRO_UPDATE_RAW_URL"
fi

# --- 7. Инструкция -----------------------------------------------------------

cat <<EOT

[maestro-install] Готово. База для maestro подготовлена.
  - agpack.yml: создан из канона maestro-install/agpack.yml / существующий
  - .opencode/: развёрнуты skills/commands/agents (agpack sync)
  - плагин: $PLUGIN_SPEC → $CONFIG_FILE
  - maestro-update.sh: загружен (для будущих обновлений maestro)

Что дальше:
  1. Запустите opencode в этом каталоге:  opencode
  2. Выполните инициализацию проекта:     /maestro-setup
     (создаёт project-context.md, maestro.json, модели агентов, каталоги)
  3. Для нового проекта — дизайн и каркас: /maestro-design
  4. Запуск фичи:                          /maestro-init "ваша задача"

Перезапустите opencode, чтобы плагин maestro-bootstrap подхватился.

Обновление maestro в дальнейшем:  bash maestro-update.sh  (см. обновление — одна команда).

Русский — рабочий язык. Источник: $REPO_URL
Обновление скрипта: $RAW_URL
EOT