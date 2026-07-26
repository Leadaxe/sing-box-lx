# TASKS 035

- [x] dnstrack: QueryTrace (write-once effective, group_path, attempts, racer), гейт по подписке
- [x] group: memberState (consecFails, lastRTT), probeMember с rtt и исходом, PushGroup, GroupState()
- [x] race: attempts из коллектора, MarkRacer, лог гонки/смены победителя
- [x] client_log: снимок трассы в QueryEvent; effective подменяет dnsServer
- [x] proto: DnsQueryEvent 13–15, DnsGroupAttempt, GetDNSGroups + 3 message (в lx-зоне)
- [x] Регенерация pb (lx-proto), откат шума и сабмодуля
- [x] daemon: handler GetDNSGroups + stub Unimplemented
- [x] libbox: DnsGroup/DnsGroupMember/итераторы; DnsQuery.GroupPath/Attempts/Racer
- [x] Лог решений: down/итог гонки/смена победителя (debug), исчерпание (warn)
- [x] Юниты по SPEC §7 (вкл. вложенную группу и write-once)
- [x] Живой box.New: медленный участник не попадает в снимок трассы
- [x] DoD: build ± теги, обе сборки §3.6, vet, gofmt
- [x] FEATURE.md DNS_GROUP + OBSERVABILITY, Roadmap, IMPLEMENTATION_REPORT.md, HISTORY.md
