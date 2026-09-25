#!/usr/bin/env bash
#
# maestro-update.sh — обновление maestro в целевом проекте.
#
# Обновляет скилы/команды/агенты (agpack sync), очищает кэш плагина OpenCode,
# мержит актуальные записи в agpack.yml.
#
# Совместимость: bash 3.2+ (macOS); Windows — через WSL/Git Bash.
#
# Флаги:
#   --pin <sha>   зафиксировать версию плагина #<sha> в конфиге opencode
#                 (по умолчанию — без пина, «последняя версия ветки»)
#   --global      пинить в глобальном конфиге opencode
#   --help        краткая справка и выход
set -euo pipefail

REPO_URL="https://github.com/wad-jet/maestro"
PLUGIN_SPEC="maestro-bootstrap@git+https://github.com/wad-jet/maestro.git"
TMP_DIR="$(mktemp -d 2>/dev/null || mktemp -d -t maestro-update)"
trap 'rm -rf "$TMP_DIR"' EXIT

SCRIPT_PATH="${BASH_SOURCE[0]}"
ORIG_ARGS=("$@")   # сохранить до парсинга флагов — для re-exec после самообновления

say()  { printf '%s\n' "$*"; }
info() { printf '\033[1;34m[maestro-update]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[maestro-update] ВНИМАНИЕ:\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m[maestro-update] ОШИБКА:\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'USAGE'
maestro-update — обновление maestro (скилы + плагин).

Использование:
  bash maestro-update.sh [--pin <sha>] [--global] [--help]

Флаги:
  --pin <sha>   зафиксировать версию плагина #<sha> в конфиге opencode.
  --global      регистрировать/пинить в глобальном конфиге (~/.config/opencode/opencode.json).
  --help        показать справку и выйти.

Предусловия: bash (macOS/Linux), git, python3 >= 3.11, agpack.
USAGE
}

PIN=""
GLOBAL_MODE=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --help|-h)   usage; exit 0 ;;
    --global)    GLOBAL_MODE=1 ;;
    --pin)       PIN="${2:-}"; shift; [[ -n "$PIN" ]] || die "--pin требует аргумент <sha>" ;;
    *)           die "неизвестный флаг: $1 (см. --help)" ;;
  esac
  shift
done

# --- 1. Preflight -----------------------------------------------------------

command -v git >/dev/null 2>&1 || die "не найден 'git'."
command -v python3 >/dev/null 2>&1 || die "не найден 'python3'."
if ! command -v agpack >/dev/null 2>&1 && [ ! -x "$HOME/.local/bin/agpack" ]; then
  die "не найден 'agpack'. Установите: uv tool install agpack  (или pipx install agpack), затем повторите."
fi
AGPACK="$(command -v agpack 2>/dev/null || echo "$HOME/.local/bin/agpack")"

# --- 2. Определение целевой версии (сеть, read-only, до мутаций) ------------

info "получаю актуальную версию из $REPO_URL..."
if [[ -n "$PIN" ]]; then
  git init -q "$TMP_DIR" && git -C "$TMP_DIR" remote add origin "$REPO_URL.git"
  git -C "$TMP_DIR" fetch -q --depth 1 origin "$PIN" || die "не удалось получить коммит $PIN"
  TARGET_VERSION="$(git -C "$TMP_DIR" show "$PIN:package.json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["version"])' 2>/dev/null)" \
    || die "в коммите $PIN нет валидного package.json с version"
  MAESTRO_INSTALL_AGPACK="$(git -C "$TMP_DIR" show "$PIN:maestro-install/agpack.yml" 2>/dev/null || true)"
else
  git clone -q --depth 1 "$REPO_URL.git" "$TMP_DIR" || die "не удалось клонировать $REPO_URL"
  TARGET_VERSION="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"])' "$TMP_DIR/package.json" 2>/dev/null)" \
    || die "в $REPO_URL нет валидного package.json с version"
  MAESTRO_INSTALL_AGPACK="$(cat "$TMP_DIR/maestro-install/agpack.yml" 2>/dev/null || true)"
fi
info "целевая версия: $TARGET_VERSION"

# --- 2a. Самопроверка версии скрипта -----------------------------------------

if [ "${MAESTRO_UPDATE_SELF_UPDATED:-}" != "1" ]; then
  info "проверяю актуальность maestro-update.sh..."
  TARGET_SCRIPT="$TMP_DIR/update-target.sh"
  git_show_ok=1
  if [[ -n "$PIN" ]]; then
    git -C "$TMP_DIR" show "$PIN:maestro-update.sh" > "$TARGET_SCRIPT" 2>/dev/null || git_show_ok=0
  else
    git -C "$TMP_DIR" show "HEAD:maestro-update.sh" > "$TARGET_SCRIPT" 2>/dev/null || git_show_ok=0
  fi
  if [ "$git_show_ok" -eq 0 ] || [ ! -s "$TARGET_SCRIPT" ]; then
    rm -f "$TARGET_SCRIPT"
    info "maestro-update.sh отсутствует в целевой версии — самопроверка пропущена"
  else
    cmp_rc=0
    cmp -s "$SCRIPT_PATH" "$TARGET_SCRIPT" || cmp_rc=$?
    case "$cmp_rc" in
      0)
        info "maestro-update.sh актуален"
        ;;
      2)
        warn "нечитаемый файл для сравнения — самопроверка пропущена"
        ;;
      *)
        if [ ! -t 0 ]; then
          die "самобновление недоступно в неинтерактивном режиме; обновите вручную: $REPO_URL (maestro-update.sh) и повторите"
        fi
        say ""
        say "[maestro-update] maestro-update.sh отличается от целевой версии ($TARGET_VERSION)."
        say "  (a) Самообновить скрипт и перезапустить (рекомендую)"
        say "  (b) Продолжить текущим скриптом"
        say "  (c) Стоп"
        while :; do
          printf '[maestro-update] выбор (a/b/c): '
          read -r choice || die "ответ не получен — остановлено"
          case "$choice" in
            a|b|c) break ;;
            *) info "введите a, b или c" ;;
          esac
        done
        case "$choice" in
          a)
            SELF_TMP="$SCRIPT_PATH.tmp.$$"
            if ! cp "$TARGET_SCRIPT" "$SELF_TMP"; then
              rm -f "$SELF_TMP"
              die "не удалось записать новый maestro-update.sh ($SCRIPT_PATH не writable?)"
            fi
            if ! mv "$SELF_TMP" "$SCRIPT_PATH"; then
              rm -f "$SELF_TMP"
              die "не удалось заменить maestro-update.sh"
            fi
            rm -rf "$TMP_DIR"
            exec env MAESTRO_UPDATE_SELF_UPDATED=1 bash "$SCRIPT_PATH" ${ORIG_ARGS[@]+"${ORIG_ARGS[@]}"}
            ;;
          b)
            warn "продолжаю текущим maestro-update.sh (расхождение не устранено)"
            ;;
          c)
            warn "останавливаюсь (выбор пользователя)"
            exit 1
            ;;
        esac
        ;;
    esac
  fi
else
  info "самопроверка пропущена (скрипт только что самообновлён)"
fi

# --- 3. Merge-add канонических записей в agpack.yml + agpack sync ------------

if [[ -n "$MAESTRO_INSTALL_AGPACK" ]]; then
  python3 - "$MAESTRO_INSTALL_AGPACK" <<'PY'
import re
import sys

# Построчный текстовый merge-add: добавляет отсутствующие (url, path)-записи из
# канонического maestro-install/agpack.yml в соответствующие секции `dependencies.*`
# проекта. Сохраняет комментарии и существующие записи без изменений. Не требует
# pyyaml (чистый текст). При ошибке — только sync (деградация из плана).
canon_text = sys.argv[1]

def parse_section(text, section):
    """Извлечь (url, path)-пары из YAML-секции без парсера."""
    recs = []
    cur = None
    for line in text.splitlines():
        m = re.match(r"^\s{4}-\s*url:\s*(\S+)", line)
        if m:
            cur = {"url": m.group(1).strip("\"'"), "path": None}
            continue
        if cur is not None and re.match(r"^\s{6}path:\s*(\S+)", line):
            cur["path"] = re.match(r"^\s{6}path:\s*(\S+)", line).group(1).strip("\"'")
        if cur is not None and cur["path"] is not None:
            recs.append(cur)
            cur = None
    return recs

# Rename-aware: безусловно удалить устаревшие записи skills/maestro-init и skills/maestro-new (M-5/M-9)
def drop_old_init(lines):
    out = []
    i = 0
    while i < len(lines):
        if re.match(r"^\s{4}-\s*url:\s*\S+", lines[i]):
            j = i + 1
            is_old = False
            while j < len(lines) and (lines[j].strip().startswith("path:") or lines[j].strip() == "" or lines[j].startswith("-")):
                if re.match(r"^\s{6}path:\s*skills/(maestro-init|maestro-new)\s*$", lines[j]):
                    is_old = True
                j += 1
            if is_old:
                i = j
                continue
        out.append(lines[i])
        i += 1
    return out

# Парсим канон по секциям.
canon_sections = {}
for sec in ("skills", "commands", "agents"):
    m = re.search(r"^\s{2}%s:" % sec, canon_text, re.M)
    if m:
        block = canon_text[m.end():]
        nxt = re.search(r"^\s{2}\w+:", block, re.M)
        block = block[:nxt.start()] if nxt else block
        canon_sections[sec] = parse_section(block, sec)

try:
    with open("agpack.yml", "r", encoding="utf-8") as f:
        lines = f.read().splitlines()
except OSError as e:
    sys.stderr.write("maestro-update: не могу прочитать agpack.yml: %s\n" % e)
    sys.exit(1)

_lines_before_drop = lines
lines = drop_old_init(lines)
dropped_init = (lines != _lines_before_drop)

def section_range(lines, section):
    """Найти диапазон строк секции `dependencies.<section>` (индексы вставки)."""
    in_deps = False
    for i, ln in enumerate(lines):
        if re.match(r"^dependencies:\s*$", ln):
            in_deps = True
            continue
        if in_deps and re.match(r"^\s{2}\w+:\s*$", ln):
            if ln.strip() == section + ":":
                j = i + 1
                while j < len(lines) and (lines[j].strip() == "" or lines[j].startswith("    ") or lines[j].startswith("#") or re.match(r"^\s{4}-", lines[j])):
                    j += 1
                return i, j
            elif ln.strip() not in ("skills:", "commands:", "agents:", "mcp:"):
                return None, None
    return None, None

changed = False
for sec, recs in canon_sections.items():
    start, end = section_range(lines, sec)
    existing = set()
    for ln in lines:
        m = re.match(r"^\s{6}path:\s*(\S+)", ln)
        if m:
            existing.add(m.group(1).strip("\"'"))
    existing.discard("skills/maestro-init")
    existing.discard("skills/maestro-new")
    if start is None:
        # Секции нет — вставить новую после `dependencies:`.
        dep_i = next((i for i, ln in enumerate(lines) if re.match(r"^dependencies:\s*$", ln)), None)
        if dep_i is None:
            continue
        # Собрать блок вставки корректно.
        add = ["  %s:" % sec]
        for r in recs:
            if r["path"] in ("skills/maestro-init", "skills/maestro-new") or r["path"] in existing:
                continue
            add.append("    - url: %s" % r["url"])
            add.append("      path: %s" % r["path"])
        if len(add) > 1:
            lines[dep_i + 1:dep_i + 1] = add
            changed = True
        continue
    add = []
    for r in recs:
        if r["path"] in ("skills/maestro-init", "skills/maestro-new") or r["path"] in existing:
            continue
        add.append("    - url: %s" % r["url"])
        add.append("      path: %s" % r["path"])
    if add:
        lines[start + 1:start + 1] = add
        changed = True

if changed or dropped_init:
    with open("agpack.yml", "w", encoding="utf-8") as f:
        f.write("\n".join(lines) + "\n")
    print("maestro-update: agpack.yml дополнен/обновлён каноническими записями")
else:
    print("maestro-update: agpack.yml актуален")
PY
else
  info "maestro-install/agpack.yml не найден в источнике — пропускаю merge-add"
fi

info "запускаю 'agpack sync'..."
"$AGPACK" sync

# --- 3a. Очистка stale-артефактов (agpack не прунит) ---
rm -rf .opencode/commands/maestro.md .opencode/skills/maestro-init .opencode/commands/maestro-new.md .opencode/skills/maestro-new

# --- 4. Очистка кэша плагина OpenCode ---------------------------------------

CACHE_BASE="${XDG_CACHE_HOME:-$HOME/.cache}/opencode/packages"
CACHE_PREFIX="maestro-bootstrap@git+https://github.com/wad-jet/maestro"
info "очищаю кэш плагина OpenCode: $CACHE_BASE/${CACHE_PREFIX}*"
MATCHED=0
if [[ -d "$CACHE_BASE" ]]; then
  for d in "$CACHE_BASE"/"$CACHE_PREFIX"*; do
    [[ -e "$d" ]] || continue
    rm -rf "$d"
    MATCHED=$((MATCHED+1))
  done
fi
if [[ "$MATCHED" -eq 0 ]]; then
  warn "кэш плагина не найден по префиксу (возможно, layout изменился) — перезапустите opencode вручную после обновления скилов"
else
  info "удалено записей кэша: $MATCHED"
fi

# --- 5. Пин версии (опционально) ---------------------------------------------

GLOBAL_CONFIG="${XDG_CONFIG_HOME:-$HOME/.config}/opencode/opencode.json"
if [[ "$GLOBAL_MODE" -eq 1 ]]; then
  CONFIG_FILE="$GLOBAL_CONFIG"
elif [[ -f "$GLOBAL_CONFIG" ]] && grep -qF "$PLUGIN_SPEC" "$GLOBAL_CONFIG"; then
  CONFIG_FILE="$GLOBAL_CONFIG"
  info "плагин найден в глобальном конфиге: $CONFIG_FILE"
else
  CONFIG_FILE=".opencode/opencode.json"
fi

python3 - "$CONFIG_FILE" "$PLUGIN_SPEC" "$PIN" <<'PY'
import json, os, sys
config_path, plugin_spec, pin = sys.argv[1], sys.argv[2], sys.argv[3] or ""
data = {}
if os.path.exists(config_path):
    try:
        with open(config_path, "r", encoding="utf-8") as f:
            c = f.read().strip()
            if c:
                data = json.loads(c)
    except (json.JSONDecodeError, OSError) as e:
        sys.stderr.write("maestro-update: не могу прочитать %s: %s\n" % (config_path, e))
        sys.exit(1)
plugins = data.get("plugin") or []
new_plugins = []
for p in plugins:
    if p == plugin_spec or p.startswith(plugin_spec + "#"):
        continue
    new_plugins.append(p)
if pin:
    new_plugins.append(plugin_spec + "#" + pin)
else:
    new_plugins.append(plugin_spec)
data["plugin"] = new_plugins
os.makedirs(os.path.dirname(config_path), exist_ok=True)
with open(config_path, "w", encoding="utf-8") as f:
    json.dump(data, f, ensure_ascii=False, indent=2)
    f.write("\n")
print("maestro-update: плагин %s%s зарегистрирован в %s" % (plugin_spec, "#"+pin if pin else "", config_path))
PY

# --- 6. Инструкция -----------------------------------------------------------

cat <<EOT

[maestro-update] Готово.
  - целевая версия: $TARGET_VERSION
  - кэш плагина очищен
  - плагин: $PLUGIN_SPEC${PIN:+#$PIN}

Что дальше:
  1. Перезапустите opencode (обязательно — плагин загрузится заново).
  2. Проверьте версию: /maestro-version  (покажет фактическую версию плагина)

Русский — рабочий язык. Источник: $REPO_URL
EOT