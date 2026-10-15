# QUIC Initial captures (calibration fixtures)

Ground-truth client Initial packets used to calibrate and verify the `ip=quic`
masquerade profiles (`quic_initial_awg.go`, `quic_clienthello_utls_awg.go`).

| File | Source |
|------|--------|
| `chrome_147_initial.bin` | Chrome 147 stable, IPv4 / UDP 443 |
| `firefox_149_initial.bin` | Firefox 149 stable, IPv4 / UDP 443 |

Each file holds the two client Initial packets of one real QUIC flow (a
ClientHello with an X25519MLKEM768 key share spans two Initials), stored as
length-prefixed records: a 2-byte big-endian length followed by the raw QUIC
Initial (UDP payload) bytes, repeated.

Origin: `boringtun/src/noise/quic/testdata/` of
[Wiresock-Foundation/wiresock-boringtun](https://github.com/Wiresock-Foundation/wiresock-boringtun)
(BSD-3-Clause), which extracted them from the WireSock `netlib` captures.
Copied unchanged on 2026-10-09. The tests decrypt them with this package's own
RFC 9001 Initial crypto (a known-answer test) and compare the extension and
transport-parameter sets of the generated ClientHellos against them.
