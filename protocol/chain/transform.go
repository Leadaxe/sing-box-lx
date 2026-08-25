package chain

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"sort"
	"strings"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"
)

// Каталог strip — односторонние клиентские приёмы, о которых сервер не знает.
// Значение — снимается ли по умолчанию при strip_evasion. Параметры-контракты
// с сервером (flow, obfs, shadowtls, plugin, udp_over_tcp, ech, пути транспортов)
// в каталог не входят и цепочкой не трогаются никогда. record_fragment — тоже
// вне каталога: под detour он включается автоматически как защита пути (SPEC 060).
var stripCatalog = map[string]bool{
	"tls.fragment":      true,
	"multiplex.padding": true,
	"xhttp.padding":     true,
	"tls.utls":          false,
}

type stripSet map[string]bool

func buildStripSet(enabled bool, patch map[string]bool) (stripSet, error) {
	set := make(stripSet)
	if enabled {
		for key, byDefault := range stripCatalog {
			if byDefault {
				set[key] = true
			}
		}
	}
	for key, value := range patch {
		if _, known := stripCatalog[key]; !known {
			return nil, E.New("strip: unknown key ", key, " (known: ", strings.Join(stripCatalogKeys(), ", "), ")")
		}
		if value {
			set[key] = true
		} else {
			delete(set, key)
		}
	}
	return set, nil
}

func stripCatalogKeys() []string {
	keys := make([]string, 0, len(stripCatalog))
	for key := range stripCatalog {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type cloneInfo struct {
	mtuConfigured uint32
	mtuEffective  uint32
	mtuReason     string
	stripped      []string
	rewritten     bool
}

func (i cloneInfo) describe() string {
	var parts []string
	if len(i.stripped) > 0 {
		parts = append(parts, "stripped="+strings.Join(i.stripped, ","))
	}
	if i.rewritten {
		parts = append(parts, "rewritten")
	}
	if i.mtuReason != "" {
		parts = append(parts, "mtu="+i.mtuReason)
	}
	if len(parts) == 0 {
		return ""
	}
	return " [" + strings.Join(parts, " ") + "]"
}

type builtOptions struct {
	typeName   string
	options    any
	isEndpoint bool
	info       cloneInfo
}

// buildCloneOptions — конфиг звена: оригинал → strip → rewrite → MTU → detour.
// Чисто по конфигу, без создания экземпляров (используется и сухим прогоном).
func (c *Chain) buildCloneOptions(position int, leaf adapter.Outbound) (*builtOptions, error) {
	typeName, m, info, err := c.leafOptionsMap(position, leaf)
	if err != nil {
		return nil, err
	}
	// listen_port у WireGuard несовместим с detour (конструктор отказывает) и
	// через звено смысла не имеет.
	if typeName == C.TypeWireGuard {
		delete(m, "listen_port")
	}
	if isTunnelType(typeName) {
		c.applyMTU(position, typeName, m, &info)
	}
	m["detour"] = c.hopTag(position - 1)
	isEndpoint := c.isEndpointLeaf(leaf)
	options, err := mapToOptions(c.ctx, typeName, m, isEndpoint)
	if err != nil {
		return nil, err
	}
	return &builtOptions{typeName: typeName, options: options, isEndpoint: isEndpoint, info: info}, nil
}

// leafOptionsMap — опции узла в виде JSON-карты с применёнными strip и rewrite
// (только для позиций ≥ 1; позиция 0 — как есть).
func (c *Chain) leafOptionsMap(position int, leaf adapter.Outbound) (string, map[string]any, cloneInfo, error) {
	var info cloneInfo
	typeName, original, err := c.optionsOf(leaf)
	if err != nil {
		return "", nil, info, err
	}
	m, err := optionsToMap(c.ctx, original)
	if err != nil {
		return "", nil, info, E.Cause(err, "serialize options of ", leaf.Tag())
	}
	if position == 0 {
		return typeName, m, info, nil
	}
	info.stripped, err = applyStrip(m, c.strip)
	if err != nil {
		return "", nil, info, E.Cause(err, leaf.Tag())
	}
	if patch, has := c.rewrite[typeName]; has {
		patchMap, isObject := patch.(map[string]any)
		if !isObject {
			return "", nil, info, E.New("rewrite[", typeName, "]: must be a JSON object")
		}
		mergePatch(m, patchMap)
		info.rewritten = true
	}
	return typeName, m, info, nil
}

func optionsToMap(ctx context.Context, options any) (map[string]any, error) {
	data, err := json.MarshalContext(ctx, options)
	if err != nil {
		return nil, err
	}
	decoder := stdjson.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var m map[string]any
	if err := decoder.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		m = make(map[string]any)
	}
	return m, nil
}

func mapToOptions(ctx context.Context, typeName string, m map[string]any, isEndpoint bool) (any, error) {
	data, err := stdjson.Marshal(m)
	if err != nil {
		return nil, err
	}
	var (
		options any
		loaded  bool
	)
	if isEndpoint {
		registry := service.FromContext[option.EndpointOptionsRegistry](ctx)
		if registry == nil {
			return nil, E.New("missing endpoint options registry")
		}
		options, loaded = registry.CreateOptions(typeName)
	} else {
		registry := service.FromContext[option.OutboundOptionsRegistry](ctx)
		if registry == nil {
			return nil, E.New("missing outbound options registry")
		}
		options, loaded = registry.CreateOptions(typeName)
	}
	if !loaded {
		return nil, E.New("unknown type: ", typeName)
	}
	if err := json.UnmarshalContextDisallowUnknownFields(ctx, data, options); err != nil {
		return nil, E.Cause(err, "decode ", typeName, " options")
	}
	return options, nil
}

// effectiveConfigJSON — SPEC 075: the link's effective config in config-file
// form ({type, tag, ...options} after strip/rewrite/MTU/detour), snapshotted at
// clone creation for GetChainCloneConfig. RunningConfig model (SPEC 037): a
// re-marshal of the parsed struct — compare semantically, not textually.
func effectiveConfigJSON(ctx context.Context, built *builtOptions, tag string) (string, error) {
	m, err := optionsToMap(ctx, built.options)
	if err != nil {
		return "", err
	}
	m["type"] = built.typeName
	m["tag"] = tag
	data, err := stdjson.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// applyStrip — снятие односторонних приёмов в карте опций. Возвращает, что
// реально снято (для наблюдаемости).
func applyStrip(m map[string]any, set stripSet) ([]string, error) {
	var stripped []string
	tls, _ := m["tls"].(map[string]any)
	if set["tls.fragment"] && tls != nil {
		_, hasDelay := tls["fragment_fallback_delay"]
		if truthy(tls["fragment"]) || hasDelay {
			delete(tls, "fragment")
			delete(tls, "fragment_fallback_delay")
			stripped = append(stripped, "tls.fragment")
		}
	}
	if set["tls.utls"] && tls != nil {
		if utls, ok := tls["utls"].(map[string]any); ok && truthy(utls["enabled"]) {
			if reality, ok := tls["reality"].(map[string]any); ok && truthy(reality["enabled"]) {
				return nil, E.New("strip tls.utls: node uses reality, which requires utls")
			}
			delete(tls, "utls")
			stripped = append(stripped, "tls.utls")
		}
	}
	if set["multiplex.padding"] {
		if mux, ok := m["multiplex"].(map[string]any); ok && truthy(mux["padding"]) {
			mux["padding"] = false
			stripped = append(stripped, "multiplex.padding")
		}
	}
	if set["xhttp.padding"] {
		if transport, ok := m["transport"].(map[string]any); ok && transport["type"] == "xhttp" {
			// Пустой x_padding_bytes означает дефолт 100-1000 — чтобы свести
			// паддинг к минимуму, диапазон задаётся явно.
			transport["x_padding_bytes"] = "1"
			delete(transport, "x_padding_obfs_mode")
			stripped = append(stripped, "xhttp.padding")
		}
	}
	return stripped, nil
}

// mergePatch — RFC 7396: null удаляет ключ, объект сливается рекурсивно,
// остальное заменяет.
func mergePatch(target map[string]any, patch map[string]any) {
	for key, value := range patch {
		if value == nil {
			delete(target, key)
			continue
		}
		if patchObject, isObject := value.(map[string]any); isObject {
			targetObject, ok := target[key].(map[string]any)
			if !ok {
				targetObject = make(map[string]any)
			}
			mergePatch(targetObject, patchObject)
			target[key] = targetObject
			continue
		}
		target[key] = value
	}
}

func truthy(value any) bool {
	b, ok := value.(bool)
	return ok && b
}
