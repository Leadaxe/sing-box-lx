printf '── ваши решения ─────────────────────────────────────────────\n'
printf '  сеть сегмента:      %s.0/24\n' "$NET_BASE"
printf '  шлюз сегмента:      %s\n' "$GW"
printf '  DHCP-пул:           %s.100–249 (lease 12h)\n' "$NET_BASE"
printf '  DNS для клиентов:   %s\n' "$SEG_DNS"
printf '  мост сегмента:      %s\n' "$BR"
printf '  uci-секция:         %s\n' "$NET"
printf '  tun-интерфейс:      %s\n' "$TUN_IF"
printf '  адрес туннеля:      %s/30\n' "$TUN_ADDR"
[ -n "$SSID_5G" ] && printf '  SSID 5 ГГц:         %s\n' "$SSID_5G"
[ -n "$SSID_2G" ] && printf '  SSID 2.4 ГГц:       %s\n' "$SSID_2G"
printf '  пароль Wi-Fi:       %s\n' "$WIFI_KEY"
printf '  порт управления:    %s\n' "$PORT"
printf '  слушает адреса:     %s\n' "$(printf '%s' "$LISTEN_ADDR" | tr -d '"')"
if [ "$WAN_EXPOSE" = 1 ]; then
    printf '  доступ из WAN:      да, порт открыт в firewall\n'
else
    printf '  доступ из WAN:      нет (только перечисленные адреса)\n'
fi
printf '─────────────────────────────────────────────────────────────\n'
printf '\n'
printf 'Pair invite:     %s:%s#%s#%s\n' "$LAN_IP" "$PORT" "$FP" "$CODE"
