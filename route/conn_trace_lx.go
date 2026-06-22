package route

import (
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// lxConnTraceDefaultTick — период тика по умолчанию, когда LX_CONN_TRACE задан
// булевым значением ("1"/"true"), а не длительностью.
const lxConnTraceDefaultTick = 5 * time.Second

// lxConnTrace читает LX_CONN_TRACE один раз и возвращает ПЕРИОД ТИКА:
//   - "", "0", "false"        → 0 (выключено: обёрток в copy-цепочке нет вовсе)
//   - "1", "true"             → дефолтный период (lxConnTraceDefaultTick)
//   - длительность ("3s","500ms") → этот период
//
// Период > 0 включает и финальный снимок, и периодический тик во время
// копирования (см. lxTraceConn.runTicker) — чтобы видеть висящий зомби в момент
// зависания, а не только постфактум при закрытии.
//
// Назначение (lx, ПРОТОТИП): развести три места застревания download, когда
// pcap показывает "данные пришли в ядро, но не дошли до приложения":
//
//	read=0            — source (remoteConn, уже расшифрованный reality/vless)
//	                    не отдал НИ ОДНОГО байта → проблема выше по стеку
//	                    (расшифровка / фрейминг proxy), а не в копировании.
//	read>0, written=0 — байты прочитаны из remote, но запись в client (gVisor
//	                    tun) заблокирована/упала → застряло именно тут.
//	read>0, written>0 — копирование шло; смотреть на err/таймаут.
//
// Гейт env-флагом, чтобы в обычных сборках обёртки в цепочке не было вовсе
// (нулевой оверхед, путь copyDirect/splice не ломается).
var lxConnTrace = sync.OnceValue(func() time.Duration {
	raw := os.Getenv("LX_CONN_TRACE")
	switch raw {
	case "", "0", "false":
		return 0
	case "1", "true":
		return lxConnTraceDefaultTick
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	// нераспознанное непустое значение → включаем с дефолтным периодом
	return lxConnTraceDefaultTick
})

// lxTraceConn — прозрачная обёртка, считающая байты Read/Write на одном конце
// соединения. НАМЕРЕННО не реализует Upstream()/ReaderReplaceable()/
// WriterReplaceable(): иначе CopyWithCounters развернёт её через
// Unwrap*Reader/Writer и скопирует мимо счётчиков (zero-copy direct-путь), и
// мы потеряем как раз ту видимость, ради которой обёртка ставится. Цена —
// принудительно буферный путь копирования на время диагностики; для прототипа
// это приемлемо и включается только под LX_CONN_TRACE.
type lxTraceConn struct {
	net.Conn
	readBytes  atomic.Int64
	writeBytes atomic.Int64
}

func newLxTraceConn(conn net.Conn) *lxTraceConn {
	return &lxTraceConn{Conn: conn}
}

func (c *lxTraceConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.readBytes.Add(int64(n))
	}
	return n, err
}

func (c *lxTraceConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.writeBytes.Add(int64(n))
	}
	return n, err
}

func (c *lxTraceConn) lxRead() int64  { return c.readBytes.Load() }
func (c *lxTraceConn) lxWrite() int64 { return c.writeBytes.Load() }
