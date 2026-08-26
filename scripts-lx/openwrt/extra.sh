EXTRA_ADDRS=""
# Кандидаты собираем в переменную (пайп в while увёл бы цикл в субшелл, где
# и ask_yn читает не тот stdin, и присвоение EXTRA_ADDRS не пережило бы выход).
_cands=$(ip -4 -o addr show 2>/dev/null \
    | sed -n 's/^[0-9]*: \([^ ]*\) *inet \([0-9.]*\)\/.*/\1 \2/p')
_offer=""
for _pair in $(printf '%s\n' "$_cands" | tr ' ' ':' | tr '\n' ' '); do
    _dev=${_pair%%:*}; _ip=${_pair##*:}
    [ -z "$_ip" ] && continue
    [ "$_ip" = "127.0.0.1" ] && continue
    [ "$_ip" = "$LAN_IP" ] && continue
    [ -n "$_wan_dev" ] && [ "$_dev" = "$_wan_dev" ] && continue
    _offer="$_offer $_dev:$_ip"
done

if [ -n "$_offer" ]; then
    say ""
    say "Кроме loopback и LAN ($LAN_IP), у роутера есть адреса:"
    for _pair in $_offer; do say "  ${_pair##*:}  (${_pair%%:*})"; done
    say "Админите роутер через WG или другой приватный канал — добавьте его адрес,"
    say "иначе управление оттуда будет недоступно."
    for _pair in $_offer; do
        _dev=${_pair%%:*}; _ip=${_pair##*:}
        ask_yn "Слушать также $_ip ($_dev)?" && EXTRA_ADDRS="$EXTRA_ADDRS $_ip"
    done
fi
