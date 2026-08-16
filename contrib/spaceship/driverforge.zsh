#
# Driverforge — active org/device section for the Spaceship prompt
#
# Shows the organization selected with `driverforge org select` and, when set, the
# active device (controller) from `driverforge device select` — the way Spaceship
# shows the git branch or the Azure subscription, e.g.:
#
#     ~/drivers/acme-thermostat  on  main   acme/living-room
#
# Install:
#   1. Source this file from your ~/.zshrc, AFTER spaceship is initialised:
#          source /path/to/cli/contrib/spaceship/driverforge.zsh
#      (or copy it into ~/.config/spaceship/ and source it there)
#   2. Add `driverforge` to your prompt order, e.g.:
#          SPACESHIP_PROMPT_ORDER=( dir git driverforge line_sep char )
#
# It calls `driverforge org current` / `driverforge device current`, which only read the
# local context file (no network) and print nothing when nothing is selected —
# so the section simply disappears when there's nothing to show. Spaceship
# renders sections asynchronously, so this never blocks your prompt.
#
# FONT: the default symbol is the Material Design "anvil" glyph (nf-md-anvil,
# U+F089B), which needs a full *Nerd Font* — a plain Powerline font (incl. plain
# Fira Code) only has the Powerline glyphs, so it will show a missing-glyph box.
# Install one and select it in your terminal, e.g.:
#       brew install --cask font-fira-code-nerd-font   # then set "FiraCode Nerd Font"
# If you'd rather not, override the symbol with an emoji (renders anywhere):
#       SPACESHIP_DRIVERFORGE_SYMBOL="🛠️ "
#

# Configuration (override any of these in ~/.zshrc before sourcing).
SPACESHIP_DRIVERFORGE_SHOW="${SPACESHIP_DRIVERFORGE_SHOW=true}"
SPACESHIP_DRIVERFORGE_SHOW_DEVICE="${SPACESHIP_DRIVERFORGE_SHOW_DEVICE=true}"
# Defaults to "using " (like the Azure section) — you're using a selected org —
# rather than the global "via " default. Override in ~/.zshrc to taste.
SPACESHIP_DRIVERFORGE_PREFIX="${SPACESHIP_DRIVERFORGE_PREFIX="using "}"
SPACESHIP_DRIVERFORGE_SUFFIX="${SPACESHIP_DRIVERFORGE_SUFFIX="$SPACESHIP_PROMPT_DEFAULT_SUFFIX"}"
SPACESHIP_DRIVERFORGE_SYMBOL=${SPACESHIP_DRIVERFORGE_SYMBOL=$'\U000F089B '} # nf-md-anvil (Nerd Font)
SPACESHIP_DRIVERFORGE_DEVICE_SEPARATOR="${SPACESHIP_DRIVERFORGE_DEVICE_SEPARATOR="/"}"
# Driverforge brand blue (matches the CLI's informational panels). Any zsh %F colour
# works: a 0-255 ANSI index or a #rrggbb truecolor value.
SPACESHIP_DRIVERFORGE_COLOR="${SPACESHIP_DRIVERFORGE_COLOR="#79C0FF"}"
# The driverforge binary to query. Override to point at a dev build, e.g.
#   SPACESHIP_DRIVERFORGE_COMMAND="driverforge-dev"
SPACESHIP_DRIVERFORGE_COMMAND="${SPACESHIP_DRIVERFORGE_COMMAND=driverforge}"

# spaceship_driverforge renders the active driverforge org (and device, if selected), or
# nothing.
spaceship_driverforge() {
  [[ $SPACESHIP_DRIVERFORGE_SHOW == false ]] && return

  # Only render when the driverforge CLI is on PATH.
  spaceship::exists "$SPACESHIP_DRIVERFORGE_COMMAND" || return

  local org
  org=$($SPACESHIP_DRIVERFORGE_COMMAND org current 2>/dev/null) || return
  [[ -z $org ]] && return

  local label="$org"
  if [[ $SPACESHIP_DRIVERFORGE_SHOW_DEVICE != false ]]; then
    local device
    device=$($SPACESHIP_DRIVERFORGE_COMMAND device current 2>/dev/null)
    [[ -n $device ]] && label="${org}${SPACESHIP_DRIVERFORGE_DEVICE_SEPARATOR}${device}"
  fi

  spaceship::section::v4 \
    --color "$SPACESHIP_DRIVERFORGE_COLOR" \
    --prefix "$SPACESHIP_DRIVERFORGE_PREFIX" \
    --suffix "$SPACESHIP_DRIVERFORGE_SUFFIX" \
    --symbol "$SPACESHIP_DRIVERFORGE_SYMBOL" \
    "$label"
}
