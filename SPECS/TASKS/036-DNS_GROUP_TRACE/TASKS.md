# TASKS 036

- [ ] dnstrack: QueryTrace (write-once effective, group_path, attempts, racer), гейт по подписке
- [ ] group: memberState (consecFails, lastRTT), probeMember с rtt и исходом, PushGroup, GroupState()
- [ ] race: attempts из коллектора, SetRacer, лог гонки/смены победителя
- [ ] client_log: снимок трассы в QueryEvent (+3 поля структуры)
- [ ] proto: DnsQueryEvent 13–15, DnsGroupAttempt, GetDNSGroups + 3 message (в lx-зоне)
- [ ] Регенерация pb (lx-proto), откат шума и сабмодуля
- [ ] daemon: handler GetDNSGroups + stub Unimplemented
- [ ] libbox: DnsGroup/DnsGroupMember/итераторы; DnsQuery.GroupPath/Attempts/Racer
- [ ] Лог решений: down/итог гонки/смена победителя (debug), исчерпание (warn)
- [ ] Юниты по SPEC §6 (вкл. вложенную группу и write-once)
- [ ] Живой box.New: медленный участник не попадает в снимок трассы
- [ ] DoD: build ± теги, обе сборки §3.6, vet, gofmt
- [ ] FEATURE.md DNS_GROUP + OBSERVABILITY: доработка, кросс-ссылки; Roadmap; IMPLEMENTATION_REPORT.md
