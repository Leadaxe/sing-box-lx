package deprecated

import (
	"fmt"

	"github.com/sagernet/sing-box/common/badversion"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/experimental/locale"
	F "github.com/sagernet/sing/common/format"

	"golang.org/x/mod/semver"
)

type Note struct {
	Name              string
	Description       string
	DeprecatedVersion string
	ScheduledVersion  string
	EnvName           string
	MigrationLink     string
}

func (n Note) Impending() bool {
	if n.ScheduledVersion == "" {
		return false
	}
	if !semver.IsValid("v" + C.Version) {
		return false
	}
	versionCurrent := badversion.Parse(C.Version)
	versionMinor := badversion.Parse(n.ScheduledVersion).Minor - versionCurrent.Minor
	if versionCurrent.PreReleaseIdentifier == "" && versionMinor < 0 {
		panic("invalid deprecated note: " + n.Name)
	}
	return versionMinor <= 1
}

func (n Note) Message() string {
	return n.MessageForLocale(locale.Current())
}

func (n Note) MessageForLocale(selectedLocale *locale.Locale) string {
	if n.MigrationLink != "" {
		return fmt.Sprintf(selectedLocale.DeprecatedMessage, n.Description, n.DeprecatedVersion, n.ScheduledVersion)
	} else {
		return fmt.Sprintf(selectedLocale.DeprecatedMessageNoLink, n.Description, n.DeprecatedVersion, n.ScheduledVersion)
	}
}

func (n Note) MessageWithLink() string {
	// lx: a note may deliberately carry no ScheduledVersion — Impending() reads
	// it as a MINOR version, so an lx-line target inside the current minor would
	// flip the warning into a hard failure (see OptionMASQUELegacyFields). Say
	// "a future version" rather than emitting "removed in sing-box ." with a
	// hole where the version should be.
	removedIn := "a future version"
	if n.ScheduledVersion != "" {
		removedIn = "sing-box " + n.ScheduledVersion
	}
	if n.MigrationLink != "" {
		return F.ToString(
			n.Description, " is deprecated in sing-box ", n.DeprecatedVersion,
			" and will be removed in ", removedIn, ", checkout documentation for migration: ", n.MigrationLink,
		)
	} else {
		return F.ToString(
			n.Description, " is deprecated in sing-box ", n.DeprecatedVersion,
			" and will be removed in ", removedIn, ".",
		)
	}
}

var OptionOutboundDNSRuleItem = Note{
	Name:              "outbound-dns-rule-item",
	Description:       "outbound DNS rule item",
	DeprecatedVersion: "1.12.0",
	ScheduledVersion:  "1.14.0",
	EnvName:           "OUTBOUND_DNS_RULE_ITEM",
	MigrationLink:     "https://sing-box.sagernet.org/migration/#migrate-outbound-dns-rule-items-to-domain-resolver",
}

var OptionMissingDomainResolver = Note{
	Name:              "missing-domain-resolver",
	Description:       "missing `route.default_domain_resolver` or `domain_resolver` in dial fields",
	DeprecatedVersion: "1.12.0",
	ScheduledVersion:  "1.14.0",
	EnvName:           "MISSING_DOMAIN_RESOLVER",
	MigrationLink:     "https://sing-box.sagernet.org/migration/#migrate-outbound-dns-rule-items-to-domain-resolver",
}

var OptionLegacyDomainStrategyOptions = Note{
	Name:              "legacy-domain-strategy-options",
	Description:       "legacy domain strategy options",
	DeprecatedVersion: "1.12.0",
	ScheduledVersion:  "1.14.0",
	EnvName:           "LEGACY_DOMAIN_STRATEGY_OPTIONS",
	MigrationLink:     "https://sing-box.sagernet.org/migration/#migrate-domain-strategy-options",
}

var OptionInlineACME = Note{
	Name:              "inline-acme-options",
	Description:       "inline ACME options in TLS",
	DeprecatedVersion: "1.14.0",
	ScheduledVersion:  "1.16.0",
	EnvName:           "INLINE_ACME_OPTIONS",
	MigrationLink:     "https://sing-box.sagernet.org/migration/#migrate-inline-acme-to-certificate-provider",
}

var OptionLegacyRuleSetDownloadDetour = Note{
	Name:              "legacy-rule-set-download-detour",
	Description:       "legacy `download_detour` remote rule-set option",
	DeprecatedVersion: "1.14.0",
	ScheduledVersion:  "1.16.0",
	EnvName:           "LEGACY_RULE_SET_DOWNLOAD_DETOUR",
}

var OptionRuleSetIPCIDRAcceptEmpty = Note{
	Name:              "dns-rule-rule-set-ip-cidr-accept-empty",
	Description:       "Legacy `rule_set_ip_cidr_accept_empty` DNS rule item",
	DeprecatedVersion: "1.14.0",
	ScheduledVersion:  "1.16.0",
	EnvName:           "DNS_RULE_RULE_SET_IP_CIDR_ACCEPT_EMPTY",
	MigrationLink:     "https://sing-box.sagernet.org/migration/#migrate-address-filter-fields-to-response-matching",
}

var OptionLegacyDNSAddressFilter = Note{
	Name:              "legacy-dns-address-filter",
	Description:       "Legacy Address Filter Fields in DNS rules",
	DeprecatedVersion: "1.14.0",
	ScheduledVersion:  "1.16.0",
	EnvName:           "LEGACY_DNS_ADDRESS_FILTER",
	MigrationLink:     "https://sing-box.sagernet.org/migration/#migrate-address-filter-fields-to-response-matching",
}

var OptionLegacyDNSRuleStrategy = Note{
	Name:              "legacy-dns-rule-strategy",
	Description:       "Legacy `strategy` DNS rule action option",
	DeprecatedVersion: "1.14.0",
	ScheduledVersion:  "1.16.0",
	EnvName:           "LEGACY_DNS_RULE_STRATEGY",
	MigrationLink:     "https://sing-box.sagernet.org/migration/#migrate-dns-rule-action-strategy-to-rule-items",
}

var OptionIndependentDNSCache = Note{
	Name:              "independent-dns-cache",
	Description:       "`independent_cache` DNS option",
	DeprecatedVersion: "1.14.0",
	ScheduledVersion:  "1.16.0",
	EnvName:           "INDEPENDENT_DNS_CACHE",
	MigrationLink:     "https://sing-box.sagernet.org/migration/#migrate-independent-dns-cache",
}

var OptionStoreRDRC = Note{
	Name:              "store-rdrc",
	Description:       "`store_rdrc` cache file option",
	DeprecatedVersion: "1.14.0",
	ScheduledVersion:  "1.16.0",
	EnvName:           "STORE_RDRC",
	MigrationLink:     "https://sing-box.sagernet.org/migration/#migrate-store-rdrc",
}

var OptionImplicitDefaultHTTPClient = Note{
	Name:              "implicit-default-http-client",
	Description:       "implicit default HTTP client using default outbound for remote rule-sets",
	DeprecatedVersion: "1.14.0",
	ScheduledVersion:  "1.16.0",
	EnvName:           "IMPLICIT_DEFAULT_HTTP_CLIENT",
}

// lx: SPEC 062 — masque predates the shared config conventions: its HTTP
// version lived in `network` (which means tcp/udp everywhere else) and its TLS settings
// were flat fields instead of the standard `tls` block. Both shapes still work,
// and using either reports this once per outbound.
// ⚠️ ScheduledVersion is deliberately left empty even though the removal target
// is known (v1.14.0-lx.30, see SPEC 062). Impending() compares MINOR versions,
// so any lx tag scheduled inside 1.14 reads as "removal is imminent" and the
// stderr manager turns the warning into a fatal error demanding
// ENABLE_DEPRECATED_MASQUE_LEGACY_FIELDS — which would break exactly the
// configs this deprecation is meant to keep working. Upstream's scheme assumes
// removals land a minor apart; the lx line numbers releases within one minor.
// The removal date lives in SPEC 062 instead.
var OptionMASQUELegacyFields = Note{
	Name:              "masque-legacy-fields",
	Description:       "legacy masque options (network, sni, skip_cert_verify, fragment*), replaced by `vhttp` and the standard `tls` block (planned for removal in v1.14.0-lx.30)",
	DeprecatedVersion: "1.14.0-lx.26",
	EnvName:           "MASQUE_LEGACY_FIELDS",
}

var Options = []Note{
	OptionMASQUELegacyFields,
	OptionOutboundDNSRuleItem,
	OptionMissingDomainResolver,
	OptionLegacyDomainStrategyOptions,
	OptionInlineACME,
	OptionLegacyRuleSetDownloadDetour,
	OptionRuleSetIPCIDRAcceptEmpty,
	OptionLegacyDNSAddressFilter,
	OptionLegacyDNSRuleStrategy,
	OptionIndependentDNSCache,
	OptionStoreRDRC,
	OptionImplicitDefaultHTTPClient,
}
