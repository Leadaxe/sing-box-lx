if [ "$WAN_IP" = "0.0.0.0" ]; then
    LISTEN_ADDR="\"0.0.0.0\""
else
    LISTEN_ADDR="\"127.0.0.1\""
    [ "$LAN_IP" != "127.0.0.1" ] && LISTEN_ADDR="$LISTEN_ADDR, \"$LAN_IP\""
    # приватные каналы админа (WG и т.п.), выбранные выше
    for _a in $EXTRA_ADDRS; do
        LISTEN_ADDR="$LISTEN_ADDR, \"$_a\""
    done
    [ "$WAN_EXPOSE" = 1 ] && [ "$WAN_IP" != "$LAN_IP" ] && LISTEN_ADDR="$LISTEN_ADDR, \"$WAN_IP\""
fi
