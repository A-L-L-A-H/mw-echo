package worldradar

import (
	"fmt"
	"strings"
)

// DomainCode identifies one of the 23 canonical domains D01-D23 (spec Section 1.1 & 1.3).
type DomainCode string

const (
	DomainGeopolitics    DomainCode = "D01"
	DomainEconomics      DomainCode = "D02"
	DomainFinance        DomainCode = "D03"
	DomainTechnology     DomainCode = "D04"
	DomainEnergy         DomainCode = "D05"
	DomainClimate        DomainCode = "D06"
	DomainHealth         DomainCode = "D07"
	DomainEnvironment    DomainCode = "D08"
	DomainTransportation DomainCode = "D09"
	DomainInfrastructure DomainCode = "D10"
	DomainAgriculture    DomainCode = "D11"
	DomainManufacturing  DomainCode = "D12"
	DomainLabor          DomainCode = "D13"
	DomainDemographics   DomainCode = "D14"
	DomainGovernance     DomainCode = "D15"
	DomainSecurity       DomainCode = "D16"
	DomainScience        DomainCode = "D17"
	DomainEducation      DomainCode = "D18"
	DomainMedia          DomainCode = "D19"
	DomainCulture        DomainCode = "D20"
	DomainSpace          DomainCode = "D21"
	DomainCyber          DomainCode = "D22"
	DomainLegal          DomainCode = "D23"
)

// LensCode identifies one of the 20 analytical lenses L01-L20 (spec Section 1.2 & 1.3).
type LensCode string

const (
	LensActor          LensCode = "L01"
	LensCapability     LensCode = "L02"
	LensIntent         LensCode = "L03"
	LensResource       LensCode = "L04"
	LensFlow           LensCode = "L05"
	LensRisk           LensCode = "L06"
	LensTime           LensCode = "L07"
	LensGeography      LensCode = "L08"
	LensInformation    LensCode = "L09"
	LensInfrastructure LensCode = "L10"
	LensPolicy         LensCode = "L11"
	LensMarket         LensCode = "L12"
	LensNarrative      LensCode = "L13"
	LensInnovation     LensCode = "L14"
	LensConnectivity   LensCode = "L15"
	LensSustainability LensCode = "L16"
	LensResilience     LensCode = "L17"
	LensEquity         LensCode = "L18"
	LensTrust          LensCode = "L19"
	LensAgency         LensCode = "L20"
)

// DomainLensMatrix represents a valid domain×lens combination with relevance weight.
type DomainLensMatrix struct {
	Domain   DomainCode `json:"domain"`
	Lens     LensCode   `json:"lens"`
	Weight   float64    `json:"weight"`   // 0.0-1.0, relevance of lens to domain
	Required bool       `json:"required"` // lens required for domain coverage
}

// TaxonomySemVer holds the semantic version v1.0.0 and source commit hash.
type TaxonomySemVer struct {
	Major int    `json:"major"`
	Minor int    `json:"minor"`
	Patch int    `json:"patch"`
	Hash  string `json:"hash"`
}

// String formats the semantic version as vMajor.Minor.Patch.
func (v TaxonomySemVer) String() string {
	return fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// DomainSpec describes a canonical taxonomy domain with its primary lenses.
type DomainSpec struct {
	Code          DomainCode `json:"code"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	PrimaryLenses []LensCode `json:"primary_lenses"`
}

// LensSpec describes a canonical analytical lens with its dimension type.
type LensSpec struct {
	Code          LensCode `json:"code"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	DimensionType string   `json:"dimension_type"`
}

// TaxonomyRegistry holds the authoritative v1.0.0 domains, lenses, and domain×lens matrix.
type TaxonomyRegistry struct {
	Version TaxonomySemVer     `json:"version"`
	Domains []DomainSpec       `json:"domains"`
	Lenses  []LensSpec         `json:"lenses"`
	Matrix  []DomainLensMatrix `json:"matrix"`

	domainMap map[DomainCode]DomainSpec
	lensMap   map[LensCode]LensSpec
	matrixMap map[DomainCode]map[LensCode]DomainLensMatrix
}

var specDomains = []DomainSpec{
	{Code: DomainGeopolitics, Name: "Geopolitics", Description: "State interactions, conflicts, treaties, diplomacy",
		PrimaryLenses: []LensCode{LensActor, LensCapability, LensIntent, LensTime, LensPolicy}},
	{Code: DomainEconomics, Name: "Economics", Description: "Macro indicators, trade, fiscal/monetary policy, markets",
		PrimaryLenses: []LensCode{LensActor, LensResource, LensFlow, LensGeography, LensMarket}},
	{Code: DomainFinance, Name: "Finance", Description: "Banking, capital markets, crypto, insurance, fintech",
		PrimaryLenses: []LensCode{LensResource, LensFlow, LensRisk, LensNarrative, LensInnovation}},
	{Code: DomainTechnology, Name: "Technology", Description: "AI, semiconductors, cyber, space, quantum, biotech",
		PrimaryLenses: []LensCode{LensCapability, LensIntent, LensInformation, LensInfrastructure, LensConnectivity}},
	{Code: DomainEnergy, Name: "Energy", Description: "Oil, gas, renewables, grid, nuclear, commodities",
		PrimaryLenses: []LensCode{LensResource, LensFlow, LensGeography, LensPolicy, LensSustainability}},
	{Code: DomainClimate, Name: "Climate", Description: "Emissions, adaptation, extreme weather, policy",
		PrimaryLenses: []LensCode{LensTime, LensPolicy, LensSustainability, LensResilience, LensEquity}},
	{Code: DomainHealth, Name: "Health", Description: "Pandemics, pharma, healthcare systems, biosecurity",
		PrimaryLenses: []LensCode{LensActor, LensTime, LensResilience, LensEquity, LensTrust}},
	{Code: DomainEnvironment, Name: "Environment", Description: "Biodiversity, pollution, water, land use, oceans",
		PrimaryLenses: []LensCode{LensSustainability, LensResilience, LensEquity, LensTrust, LensAgency}},
	{Code: DomainTransportation, Name: "Transportation", Description: "Aviation, maritime, rail, logistics, autonomous",
		PrimaryLenses: []LensCode{LensCapability, LensIntent, LensInfrastructure, LensPolicy, LensConnectivity}},
	{Code: DomainInfrastructure, Name: "Infrastructure",
		Description: "Construction, utilities, digital infra, smart cities", PrimaryLenses: []LensCode{LensIntent,
			LensInfrastructure, LensPolicy, LensConnectivity, LensSustainability}},
	{Code: DomainAgriculture, Name: "Agriculture", Description: "Food security, commodities, agtech, supply chains",
		PrimaryLenses: []LensCode{LensResource, LensFlow, LensGeography, LensPolicy, LensSustainability}},
	{Code: DomainManufacturing, Name: "Manufacturing",
		Description: "Industrial output, supply chains, robotics, 3D printing", PrimaryLenses: []LensCode{LensIntent,
			LensInfrastructure, LensPolicy, LensConnectivity, LensSustainability}},
	{Code: DomainLabor, Name: "Labor", Description: "Employment, wages, unions, migration, skills",
		PrimaryLenses: []LensCode{LensActor, LensTime, LensPolicy, LensMarket, LensTrust}},
	{Code: DomainDemographics, Name: "Demographics", Description: "Population, urbanization, aging, migration flows",
		PrimaryLenses: []LensCode{LensActor, LensTime, LensPolicy, LensResilience, LensTrust}},
	{Code: DomainGovernance, Name: "Governance", Description: "Institutions, regulation, corruption, rule of law",
		PrimaryLenses: []LensCode{LensActor, LensCapability, LensTime, LensPolicy, LensMarket}},
	{Code: DomainSecurity, Name: "Security", Description: "Defense, terrorism, cyber warfare, intelligence",
		PrimaryLenses: []LensCode{LensCapability, LensIntent, LensTime, LensPolicy, LensConnectivity}},
	{Code: DomainScience, Name: "Science", Description: "Research, publications, funding, breakthroughs",
		PrimaryLenses: []LensCode{LensCapability, LensInformation, LensInfrastructure, LensInnovation, LensEquity}},
	{Code: DomainEducation, Name: "Education", Description: "Systems, policy, edtech, skills, research",
		PrimaryLenses: []LensCode{LensActor, LensInformation, LensInfrastructure, LensInnovation, LensTrust}},
	{Code: DomainMedia, Name: "Media", Description: "Information flows, disinformation, platforms, journalism",
		PrimaryLenses: []LensCode{LensActor, LensCapability, LensIntent, LensInformation, LensNarrative}},
	{Code: DomainCulture, Name: "Culture", Description: "Arts, heritage, religion, social movements, identity",
		PrimaryLenses: []LensCode{LensTime, LensInformation, LensInnovation, LensResilience, LensTrust}},
	{Code: DomainSpace, Name: "Space", Description: "Launch, satellites, exploration, commercial, policy",
		PrimaryLenses: []LensCode{LensCapability, LensIntent, LensInformation, LensInfrastructure, LensConnectivity}},
	{Code: DomainCyber, Name: "Cyber", Description: "Threats, vulnerabilities, incidents, policy, crypto",
		PrimaryLenses: []LensCode{LensCapability, LensIntent, LensInformation, LensInfrastructure, LensNarrative}},
	{Code: DomainLegal, Name: "Legal", Description: "Litigation, precedent, regulatory enforcement, intl law",
		PrimaryLenses: []LensCode{LensActor, LensTime, LensPolicy, LensMarket, LensInnovation}},
}

var specLenses = []LensSpec{
	{Code: LensActor, Name: "Actor", Description: "State, org, individual, coalition", DimensionType: "Entity resolution"},
	{Code: LensCapability, Name: "Capability", Description: "Military, tech, economic, institutional",
		DimensionType: "Numeric + categorical"},
	{Code: LensIntent, Name: "Intent", Description: "Strategic, tactical, declarative, covert",
		DimensionType: "Categorical + confidence"},
	{Code: LensResource, Name: "Resource", Description: "Capital, commodities, energy, compute",
		DimensionType: "Numeric + unit"},
	{Code: LensFlow, Name: "Flow", Description: "Trade, capital, migration, information",
		DimensionType: "Network + volume"},
	{Code: LensRisk, Name: "Risk", Description: "Probability × impact, tail events, contagion",
		DimensionType: "Numeric [0,1]"},
	{Code: LensTime, Name: "Time", Description: "Horizon, urgency, cyclicality, sequence",
		DimensionType: "Temporal interval"},
	{Code: LensGeography, Name: "Geography", Description: "Location, region, corridor, chokepoint",
		DimensionType: "Geospatial + precision"},
	{Code: LensInformation, Name: "Information", Description: "Signal quality, disinformation, opacity, latency",
		DimensionType: "Categorical + numeric"},
	{Code: LensInfrastructure, Name: "Infrastructure", Description: "Physical, digital, critical, resilient",
		DimensionType: "Entity + status"},
	{Code: LensPolicy, Name: "Policy", Description: "Law, regulation, treaty, sanction, incentive",
		DimensionType: "Categorical + text"},
	{Code: LensMarket, Name: "Market", Description: "Price, volume, liquidity, sentiment, structure",
		DimensionType: "Numeric + time-series"},
	{Code: LensNarrative, Name: "Narrative", Description: "Framing, propaganda, meme, discourse",
		DimensionType: "Text + embeddings"},
	{Code: LensInnovation, Name: "Innovation", Description: "R&D, patents, adoption, diffusion, disruption",
		DimensionType: "Numeric + categorical"},
	{Code: LensConnectivity, Name: "Connectivity", Description: "Network topology, redundancy, dependency",
		DimensionType: "Graph + metrics"},
	{Code: LensSustainability, Name: "Sustainability", Description: "Emissions, circularity, planetary boundaries",
		DimensionType: "Numeric + targets"},
	{Code: LensResilience, Name: "Resilience", Description: "Redundancy, adaptability, recovery, antifragility",
		DimensionType: "Numeric + scenario"},
	{Code: LensEquity, Name: "Equity", Description: "Distribution, access, justice, inclusion",
		DimensionType: "Numeric + categorical"},
	{Code: LensTrust, Name: "Trust", Description: "Legitimacy, credibility, social capital, institutions",
		DimensionType: "Numeric [0,1]"},
	{Code: LensAgency, Name: "Agency", Description: "Autonomy, decision rights, empowerment, capture",
		DimensionType: "Categorical + numeric"},
}

// NewTaxonomyRegistry constructs the v1.0.0 TaxonomyRegistry with all 23 domains,
// 20 lenses, and the DomainLensMatrix.
func NewTaxonomyRegistry(sourceHash string) *TaxonomyRegistry {
	reg := &TaxonomyRegistry{
		Version: TaxonomySemVer{
			Major: 1,
			Minor: 0,
			Patch: 0,
			Hash:  strings.TrimSpace(sourceHash),
		},
		Domains:   make([]DomainSpec, len(specDomains)),
		Lenses:    make([]LensSpec, len(specLenses)),
		domainMap: make(map[DomainCode]DomainSpec, len(specDomains)),
		lensMap:   make(map[LensCode]LensSpec, len(specLenses)),
		matrixMap: make(map[DomainCode]map[LensCode]DomainLensMatrix, len(specDomains)),
	}
	copy(reg.Domains, specDomains)
	copy(reg.Lenses, specLenses)

	for _, d := range specDomains {
		lensesCopy := make([]LensCode, len(d.PrimaryLenses))
		copy(lensesCopy, d.PrimaryLenses)
		d.PrimaryLenses = lensesCopy
		reg.domainMap[d.Code] = d
		reg.matrixMap[d.Code] = make(map[LensCode]DomainLensMatrix, len(specLenses))

		primarySet := make(map[LensCode]int, len(d.PrimaryLenses))
		for idx, l := range d.PrimaryLenses {
			primarySet[l] = idx
		}
		for _, l := range specLenses {
			weight := 0.35
			required := false
			if rank, ok := primarySet[l.Code]; ok {
				weight = 1.0 - float64(rank)*0.08
				required = rank < 2
			}
			entry := DomainLensMatrix{
				Domain:   d.Code,
				Lens:     l.Code,
				Weight:   weight,
				Required: required,
			}
			reg.Matrix = append(reg.Matrix, entry)
			reg.matrixMap[d.Code][l.Code] = entry
		}
	}

	for _, l := range specLenses {
		reg.lensMap[l.Code] = l
	}
	return reg
}

// IsValidDomain reports whether code is one of D01-D23.
func (r *TaxonomyRegistry) IsValidDomain(code DomainCode) bool {
	norm := DomainCode(strings.ToUpper(strings.TrimSpace(string(code))))
	_, ok := r.domainMap[norm]
	return ok
}

// IsValidLens reports whether code is one of L01-L20.
func (r *TaxonomyRegistry) IsValidLens(code LensCode) bool {
	norm := LensCode(strings.ToUpper(strings.TrimSpace(string(code))))
	_, ok := r.lensMap[norm]
	return ok
}

// Domain returns the DomainSpec for a DomainCode.
func (r *TaxonomyRegistry) Domain(code DomainCode) (DomainSpec, bool) {
	norm := DomainCode(strings.ToUpper(strings.TrimSpace(string(code))))
	d, ok := r.domainMap[norm]
	return d, ok
}

// Lens returns the LensSpec for a LensCode.
func (r *TaxonomyRegistry) Lens(code LensCode) (LensSpec, bool) {
	norm := LensCode(strings.ToUpper(strings.TrimSpace(string(code))))
	l, ok := r.lensMap[norm]
	return l, ok
}

// MatrixWeight returns the relevance entry for a (DomainCode, LensCode) pair.
func (r *TaxonomyRegistry) MatrixWeight(domain DomainCode, lens LensCode) (DomainLensMatrix, bool) {
	dNorm := DomainCode(strings.ToUpper(strings.TrimSpace(string(domain))))
	lNorm := LensCode(strings.ToUpper(strings.TrimSpace(string(lens))))
	byLens, ok := r.matrixMap[dNorm]
	if !ok {
		return DomainLensMatrix{}, false
	}
	entry, ok := byLens[lNorm]
	return entry, ok
}
