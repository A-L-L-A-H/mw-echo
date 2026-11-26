package worldradar

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// TaxonomyVersion identifies the canonical R!OTS World Radar Network taxonomy release.
const TaxonomyVersion = "1.0.0"

// Domain describes one of the 23 canonical world-intelligence domains.
type Domain struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Niches      []string `json:"niches"`
}

// DetectionLens describes an analytical lens used to classify radar signals.
type DetectionLens struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// PackManifest defines a validated Alpha Radar Pack binding domains and lenses.
type PackManifest struct {
	ID          string   `json:"id"`
	Version     string   `json:"version"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Domains     []string `json:"domains"`
	Lenses      []string `json:"lenses"`
	DefaultMode string   `json:"defaultMode"`
}

var packIDPattern = regexp.MustCompile(`^radar\.[a-z0-9]+(?:-[a-z0-9]+)*$`)

var canonicalDomains = []Domain{
	{ID: "D01", Name: "World & Geopolitical", Description: "Sovereign diplomacy, conflicts, sanctions, treaties, and cross-border stability.", Niches: []string{"diplomacy", "sanctions", "regional-security", "elections", "maritime-chokepoints"}},
	{ID: "D02", Name: "Government/Law/Regulation", Description: "Legislation, court rulings, agency rulemaking, antitrust, and statutory enforcement.", Niches: []string{"legislation", "antitrust", "privacy-law", "licensing", "judicial-rulings"}},
	{ID: "D03", Name: "Macroeconomy", Description: "Central bank policy, inflation, sovereign debt, labor markets, and fiscal indicators.", Niches: []string{"monetary-policy", "inflation", "labor", "sovereign-debt", "gdp-trade"}},
	{ID: "D04", Name: "Financial Markets", Description: "Equities, fixed income, FX, credit spreads, liquidity events, and market structure.", Niches: []string{"equities", "fixed-income", "fx", "credit-risk", "market-infrastructure"}},
	{ID: "D05", Name: "Business/Corporate", Description: "M&A, restructurings, leadership changes, earnings warnings, and corporate governance.", Niches: []string{"mergers-acquisitions", "governance", "restructuring", "earnings", "supply-contracts"}},
	{ID: "D06", Name: "AI & Autonomous Systems", Description: "Foundation models, agent frameworks, robotics, benchmarks, and AI safety governance.", Niches: []string{"agent-frameworks", "foundation-models", "robotics", "inference-tooling", "ai-governance"}},
	{ID: "D07", Name: "Software/Open Source", Description: "Developer ecosystems, OSS releases, package registries, licenses, and runtime tooling.", Niches: []string{"oss-releases", "package-registries", "compilers-runtimes", "dev-infrastructure", "oss-licensing"}},
	{ID: "D08", Name: "Semiconductor", Description: "Foundries, lithography, packaging, fab capacity, export controls, and chip architectures.", Niches: []string{"foundry-nodes", "packaging", "eda-ip", "export-controls", "memory-compute"}},
	{ID: "D09", Name: "Cloud/Internet/Telecom/ISP", Description: "Hyperscalers, BGP routing, DNS, submarine cables, CDN outages, and carrier networks.", Niches: []string{"cloud-regions", "bgp-routing", "dns-tls", "cdn-edge", "telecom-carriers"}},
	{ID: "D10", Name: "Cybersecurity", Description: "CVEs, active exploitation, advisories, supply-chain compromises, and defensive patching.", Niches: []string{"cve-advisories", "zero-days", "supply-chain-sec", "identity-iam", "incident-response"}},
	{ID: "D11", Name: "Media/Social", Description: "Information ecosystems, platform policy shifts, synthetic media, and narrative velocity.", Niches: []string{"platform-policy", "information-ops", "creator-platforms", "journalism", "moderation"}},
	{ID: "D12", Name: "Science/Research", Description: "Peer-reviewed breakthroughs, preprints, national labs, and instrumentation milestones.", Niches: []string{"physics-materials", "biotech-basic", "climate-science", "computing-theory", "scientific-grants"}},
	{ID: "D13", Name: "Health/Medicine", Description: "Public health alerts, clinical trials, drug approvals, diagnostics, and hospital capacity.", Niches: []string{"epidemiology", "clinical-trials", "regulatory-approvals", "medical-devices", "health-systems"}},
	{ID: "D14", Name: "Earth/Climate", Description: "Extreme weather, seismic activity, hydrology, ocean anomalies, and climate tipping signals.", Niches: []string{"extreme-weather", "seismic-volcanic", "hydrology", "wildfire", "climate-anomalies"}},
	{ID: "D15", Name: "Energy/Minerals", Description: "Grid reliability, oil and gas flows, renewables, nuclear capacity, and critical minerals.", Niches: []string{"power-grids", "hydrocarbons", "renewables-storage", "nuclear", "critical-minerals"}},
	{ID: "D16", Name: "Global Trade/Supply Chain", Description: "Port congestion, shipping lanes, customs tariffs, freight rates, and industrial inputs.", Niches: []string{"maritime-freight", "port-operations", "tariffs-customs", "air-cargo", "component-shortages"}},
	{ID: "D17", Name: "Cities/Real Estate", Description: "Urban infrastructure, zoning, transit disruptions, commercial real estate, and housing.", Niches: []string{"urban-transit", "zoning-permits", "commercial-re", "housing-supply", "municipal-infra"}},
	{ID: "D18", Name: "Humanitarian/Social Dev", Description: "Displacement, disaster relief logistics, aid corridors, and civic resilience indicators.", Niches: []string{"disaster-relief", "displacement", "aid-corridors", "public-services", "civic-resilience"}},
	{ID: "D19", Name: "Agriculture/Food/Water", Description: "Crop yields, fertilizer inputs, aquifer levels, food safety recalls, and commodity supply.", Niches: []string{"crop-forecasts", "water-security", "fertilizer-inputs", "food-safety", "grain-logistics"}},
	{ID: "D20", Name: "Culture/Religion/Ethics", Description: "Normative debates, cultural institutions, digital ethics, and heritage preservation.", Niches: []string{"digital-ethics", "cultural-heritage", "civic-norms", "academic-freedom", "public-trust"}},
	{ID: "D21", Name: "Consumer/Retail", Description: "Consumer demand shifts, retail closures, product recalls, and e-commerce logistics.", Niches: []string{"retail-demand", "product-recalls", "ecommerce", "payments-pos", "consumer-pricing"}},
	{ID: "D22", Name: "Entertainment/Gaming/Sports", Description: "Media rights, game engines, streaming platforms, live events, and studio consolidation.", Niches: []string{"game-engines", "streaming-media", "sports-rights", "live-events", "creator-economies"}},
	{ID: "D23", Name: "Space/Aerospace", Description: "Launch manifests, orbital constellations, aviation advisories, and aerospace certification.", Niches: []string{"orbital-launches", "satellite-comms", "aviation-safety", "space-debris", "aerospace-mfg"}},
}

var canonicalLenses = []DetectionLens{
	{ID: "L01", Name: "Trend", Description: "Sustained directional movement across multiple observation windows."},
	{ID: "L02", Name: "Emerging Signal", Description: "Early weak-to-moderate signal before mainstream consensus forms."},
	{ID: "L03", Name: "Anomaly", Description: "Statistically or structurally unusual deviation from baseline behavior."},
	{ID: "L04", Name: "Break / Change", Description: "Discrete state transition, breaking change, or regime shift."},
	{ID: "L05", Name: "Risk / Threat", Description: "Adverse exposure, hazard escalation, or downside operational threat."},
	{ID: "L06", Name: "Opportunity", Description: "Favorable structural opening, capability unlock, or efficiency gain."},
	{ID: "L07", Name: "Ecosystem Shift", Description: "Realignment of standards, tooling, dependencies, or market participants."},
	{ID: "L08", Name: "Narrative Shift", Description: "Measurable change in framing, salience, or public positioning."},
	{ID: "L09", Name: "Sentiment Shift", Description: "Rapid polarity inflection across authoritative or community observers."},
	{ID: "L10", Name: "Momentum / Acceleration", Description: "Second-derivative increase in publication, adoption, or incident rate."},
	{ID: "L11", Name: "Innovation / Adoption", Description: "New technical capability crossing into practical deployment."},
	{ID: "L12", Name: "Compliance / Enforcement", Description: "Binding regulatory action, audit directive, or statutory enforcement."},
	{ID: "L13", Name: "Market Repricing", Description: "Rapid valuation, spread, or cost curve adjustment to new facts."},
	{ID: "L14", Name: "Disruption / Outage", Description: "Service degradation, infrastructure outage, or physical flow interruption."},
	{ID: "L15", Name: "Cross-Border Spillover", Description: "Localized shock propagating across jurisdictions or supply tiers."},
	{ID: "L16", Name: "Concentration / Dependency", Description: "Single-point-of-failure reliance across vendors, routes, or protocols."},
	{ID: "L17", Name: "Vulnerability / Exposure", Description: "Newly disclosed flaw, misconfiguration, or unmitigated attack surface."},
	{ID: "L18", Name: "Release / Milestone", Description: "Versioned software release, specification freeze, or formal launch."},
	{ID: "L19", Name: "Policy / Mandate", Description: "Formal policy announcement, procurement rule, or compliance deadline."},
	{ID: "L20", Name: "Coverage Gap", Description: "Under-reported or blind-spot area requiring independent corroboration."},
}

var alphaPacks = []PackManifest{
	{
		ID:          "radar.open-source-dev",
		Version:     TaxonomyVersion,
		Name:        "Open Source Dev Radar",
		Description: "Tracks OSS releases, breaking ecosystem shifts, and developer adoption velocity.",
		Domains:     []string{"D07"},
		Lenses:      []string{"L01", "L04", "L07", "L11", "L18"},
		DefaultMode: "monitor",
	},
	{
		ID:          "radar.ai-agent-discovery",
		Version:     TaxonomyVersion,
		Name:        "AI Agent Discovery Radar",
		Description: "Monitors autonomous agent frameworks, model capabilities, and OSS AI tooling.",
		Domains:     []string{"D06", "D07"},
		Lenses:      []string{"L01", "L02", "L04", "L11", "L18"},
		DefaultMode: "monitor",
	},
	{
		ID:          "radar.cloud-isp-infra",
		Version:     TaxonomyVersion,
		Name:        "Cloud & ISP Infra Radar",
		Description: "Detects cloud region outages, routing anomalies, and defensive network posture shifts.",
		Domains:     []string{"D09", "D10"},
		Lenses:      []string{"L03", "L05", "L14", "L17", "L19"},
		DefaultMode: "alert",
	},
	{
		ID:          "radar.world-events",
		Version:     TaxonomyVersion,
		Name:        "World Events Radar",
		Description: "Tracks geopolitical shocks, climate disruptions, supply-chain spillovers, and coverage gaps.",
		Domains:     []string{"D01", "D14", "D16"},
		Lenses:      []string{"L02", "L08", "L14", "L15", "L20"},
		DefaultMode: "monitor",
	},
	{
		ID:          "radar.cyber-defensive",
		Version:     TaxonomyVersion,
		Name:        "Cyber Defensive Radar",
		Description: "Monitors defensive advisories, CVEs, package compromises, and mitigation mandates.",
		Domains:     []string{"D10", "D07"},
		Lenses:      []string{"L04", "L05", "L12", "L14", "L17"},
		DefaultMode: "alert",
	},
	{
		ID:          "radar.law-regulation",
		Version:     TaxonomyVersion,
		Name:        "Law & Regulation Radar",
		Description: "Tracks regulatory enforcement, statutory changes, and corporate compliance mandates.",
		Domains:     []string{"D02", "D05"},
		Lenses:      []string{"L04", "L05", "L12", "L19"},
		DefaultMode: "monitor",
	},
	{
		ID:          "radar.social-trends",
		Version:     TaxonomyVersion,
		Name:        "Social & Media Narrative Radar",
		Description: "Monitors social narrative velocity, sentiment shifts, and platform discourse trends.",
		Domains:     []string{"D11"},
		Lenses:      []string{"L01", "L02", "L08", "L09", "L10"},
		DefaultMode: "monitor",
	},
	{
		ID:          "radar.business-competitor",
		Version:     TaxonomyVersion,
		Name:        "Business & Competitor Radar",
		Description: "Tracks corporate moves, competitor releases, ecosystem shifts, and market repricing.",
		Domains:     []string{"D05"},
		Lenses:      []string{"L04", "L06", "L07", "L13", "L18"},
		DefaultMode: "monitor",
	},
}

var domainIndex = buildDomainIndex()
var lensIndex = buildLensIndex()
var packIndex = buildPackIndex()

func buildDomainIndex() map[string]Domain {
	idx := make(map[string]Domain, len(canonicalDomains))
	for _, dom := range canonicalDomains {
		idx[dom.ID] = dom
	}
	return idx
}

func buildLensIndex() map[string]DetectionLens {
	idx := make(map[string]DetectionLens, len(canonicalLenses))
	for _, lens := range canonicalLenses {
		idx[lens.ID] = lens
	}
	return idx
}

func buildPackIndex() map[string]PackManifest {
	idx := make(map[string]PackManifest, len(alphaPacks))
	for _, pack := range alphaPacks {
		idx[pack.ID] = pack
	}
	return idx
}

// AllDomains returns a defensive copy of all 23 canonical domains.
func AllDomains() []Domain {
	out := make([]Domain, len(canonicalDomains))
	for i, dom := range canonicalDomains {
		niches := make([]string, len(dom.Niches))
		copy(niches, dom.Niches)
		dom.Niches = niches
		out[i] = dom
	}
	return out
}

// GetDomain looks up a canonical domain by ID (case-insensitive).
func GetDomain(domainID string) (Domain, bool) {
	dom, ok := domainIndex[strings.ToUpper(strings.TrimSpace(domainID))]
	if !ok {
		return Domain{}, false
	}
	niches := make([]string, len(dom.Niches))
	copy(niches, dom.Niches)
	dom.Niches = niches
	return dom, true
}

// AllLenses returns a defensive copy of all 20 detection lenses.
func AllLenses() []DetectionLens {
	out := make([]DetectionLens, len(canonicalLenses))
	copy(out, canonicalLenses)
	return out
}

// GetLens looks up a detection lens by ID (case-insensitive).
func GetLens(lensID string) (DetectionLens, bool) {
	lens, ok := lensIndex[strings.ToUpper(strings.TrimSpace(lensID))]
	return lens, ok
}

// AllPacks returns all validated Alpha Radar Packs.
func AllPacks() []PackManifest {
	out := make([]PackManifest, len(alphaPacks))
	for i, pack := range alphaPacks {
		out[i] = clonePack(pack)
	}
	return out
}

// GetPack returns an Alpha Radar Pack manifest by ID.
func GetPack(packID string) (PackManifest, bool) {
	pack, ok := packIndex[strings.ToLower(strings.TrimSpace(packID))]
	if !ok {
		return PackManifest{}, false
	}
	return clonePack(pack), true
}

func clonePack(pack PackManifest) PackManifest {
	domains := make([]string, len(pack.Domains))
	copy(domains, pack.Domains)
	lenses := make([]string, len(pack.Lenses))
	copy(lenses, pack.Lenses)
	pack.Domains = domains
	pack.Lenses = lenses
	return pack
}

// ValidatePack verifies that a PackManifest has a valid pack ID, non-empty metadata,
// and exclusively references known canonical domains and detection lenses.
func ValidatePack(manifest PackManifest) error {
	cleanID := strings.TrimSpace(manifest.ID)
	if !packIDPattern.MatchString(cleanID) {
		return fmt.Errorf("invalid pack id %q: must match radar.<slug>", manifest.ID)
	}
	if strings.TrimSpace(manifest.Name) == "" {
		return errors.New("pack name is required")
	}
	if len(manifest.Domains) == 0 {
		return errors.New("pack must declare at least one domain")
	}
	if len(manifest.Lenses) == 0 {
		return errors.New("pack must declare at least one detection lens")
	}
	seenDomains := make(map[string]bool, len(manifest.Domains))
	for _, domID := range manifest.Domains {
		norm := strings.ToUpper(strings.TrimSpace(domID))
		if _, ok := domainIndex[norm]; !ok {
			return fmt.Errorf("unknown domain %q in pack %q", domID, cleanID)
		}
		if seenDomains[norm] {
			return fmt.Errorf("duplicate domain %q in pack %q", domID, cleanID)
		}
		seenDomains[norm] = true
	}
	seenLenses := make(map[string]bool, len(manifest.Lenses))
	for _, lensID := range manifest.Lenses {
		norm := strings.ToUpper(strings.TrimSpace(lensID))
		if _, ok := lensIndex[norm]; !ok {
			return fmt.Errorf("unknown detection lens %q in pack %q", lensID, cleanID)
		}
		if seenLenses[norm] {
			return fmt.Errorf("duplicate detection lens %q in pack %q", lensID, cleanID)
		}
		seenLenses[norm] = true
	}
	return nil
}
