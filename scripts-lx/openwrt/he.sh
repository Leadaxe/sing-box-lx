say()  { printf '%s\n' "$*"; }
warn() { printf '!! %s\n' "$*" >&2; }
ask_yn() { printf '%s [y/N]: ' "$1" >&2; case "$YNLIST" in *"$1"*) return 1;; esac
           [ "$YNALL" = y ]; }
ip() { printf '%s\n' "$FAKE_OADDR"; }
