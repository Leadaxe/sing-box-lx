package outbound

import (
	"os"

	"github.com/sagernet/sing-box/adapter"
	E "github.com/sagernet/sing/common/exceptions"
)

// lx: SPEC 073 — память опций и внутренние outbound'ы для цепочки.

var (
	_ adapter.OutboundOptionsProvider   = (*Manager)(nil)
	_ adapter.InternalOutboundRegistrar = (*Manager)(nil)
)

type managedOptions struct {
	outboundType string
	options      any
}

// OptionsOf возвращает тип и опции, из которых был создан outbound с тегом tag.
// Опции — тот же объект, что ушёл в конструктор; вызывающий обязан копировать.
func (m *Manager) OptionsOf(tag string) (string, any, bool) {
	m.access.RLock()
	defer m.access.RUnlock()
	item, loaded := m.optionsByTag[tag]
	if !loaded {
		return "", nil, false
	}
	return item.outboundType, item.options, true
}

// AddInternal регистрирует внутренний outbound: он резолвится Outbound(tag)
// (детур звеньев, URLTest по тегу), но не входит в Outbounds() и не управляется
// жизненным циклом менеджера. Коллизия с пользовательским тегом — ошибка.
func (m *Manager) AddInternal(outbound adapter.Outbound) error {
	tag := outbound.Tag()
	if tag == "" {
		return os.ErrInvalid
	}
	m.access.Lock()
	defer m.access.Unlock()
	if _, exists := m.outboundByTag[tag]; exists {
		return E.New("internal outbound tag collides with outbound: ", tag)
	}
	if _, exists := m.internalByTag[tag]; exists {
		return E.New("internal outbound already registered: ", tag)
	}
	if m.endpoint != nil {
		if _, exists := m.endpoint.Get(tag); exists {
			return E.New("internal outbound tag collides with endpoint: ", tag)
		}
	}
	m.internalByTag[tag] = outbound
	return nil
}

func (m *Manager) RemoveInternal(tag string) {
	m.access.Lock()
	defer m.access.Unlock()
	delete(m.internalByTag, tag)
}
