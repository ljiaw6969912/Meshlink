package p2p

import (
	"net/netip"
	"strconv"
	"strings"
)

type NATType string

const (
	NATTypeUnknown            NATType = "unknown"
	NATTypeOpenInternet       NATType = "open_internet"
	NATTypeFullCone           NATType = "full_cone"
	NATTypeRestrictedCone     NATType = "restricted_cone"
	NATTypePortRestrictedCone NATType = "port_restricted_cone"
	NATTypeSymmetric          NATType = "symmetric_nat"
	NATTypeUDPBlocked         NATType = "udp_blocked"
	NATTypeProbeFailed        NATType = "probe_failed"
)

type NATProbeObservation struct {
	ProbeServer            string `json:"probe_server,omitempty"`
	LocalAddress           string `json:"local_address,omitempty"`
	LocalPort              int    `json:"local_port,omitempty"`
	MappedAddress          string `json:"mapped_address,omitempty"`
	MappedPort             int    `json:"mapped_port,omitempty"`
	ResponseReceived       bool   `json:"response_received,omitempty"`
	ChangedAddressResponse bool   `json:"changed_address_response,omitempty"`
	ChangedPortResponse    bool   `json:"changed_port_response,omitempty"`
	ProbeError             string `json:"probe_error,omitempty"`
}

type NATProbeSummary struct {
	Type                   NATType `json:"type"`
	UDPAvailable           bool    `json:"udp_available"`
	MappingStable          bool    `json:"mapping_stable"`
	HolePunchRecommended   bool    `json:"hole_punch_recommended"`
	RelayRecommended       bool    `json:"relay_recommended"`
	SuccessfulObservations int     `json:"successful_observations,omitempty"`
	Reason                 string  `json:"reason,omitempty"`
}

func CloneNATProbeSummary(summary *NATProbeSummary) *NATProbeSummary {
	if summary == nil {
		return nil
	}
	normalized := NormalizeNATProbeSummary(*summary)
	return &normalized
}

func NormalizeNATProbeSummary(summary NATProbeSummary) NATProbeSummary {
	successfulObservations := summary.SuccessfulObservations
	if successfulObservations < 0 {
		successfulObservations = 0
	}
	switch summary.Type {
	case NATTypeOpenInternet:
		return natProbeSummary(summary.Type, true, true, true, false, successfulObservations, "UDP endpoint is directly reachable without NAT")
	case NATTypeFullCone:
		return natProbeSummary(summary.Type, true, true, true, false, successfulObservations, "stable mapping accepts changed address probes; try hole punching")
	case NATTypeRestrictedCone:
		return natProbeSummary(summary.Type, true, true, true, false, successfulObservations, "stable mapping accepts changed port probes; try hole punching")
	case NATTypePortRestrictedCone:
		return natProbeSummary(summary.Type, true, true, true, false, successfulObservations, "stable mapping is port restricted; try coordinated hole punching with Relay fallback ready")
	case NATTypeSymmetric:
		return natProbeSummary(summary.Type, true, false, false, true, successfulObservations, "external UDP mapping changed across probe servers; prefer Relay fallback")
	case NATTypeUDPBlocked:
		return natProbeSummary(summary.Type, false, false, false, true, successfulObservations, "no UDP probe responses were received; use Relay fallback")
	case NATTypeProbeFailed:
		return natProbeSummary(summary.Type, false, false, false, true, successfulObservations, "NAT probe could not complete; use Relay fallback")
	default:
		return natProbeSummary(NATTypeUnknown, false, false, false, false, successfulObservations, "no NAT probe observations were provided")
	}
}

func ClassifyNATProbe(observations []NATProbeObservation) NATProbeSummary {
	if len(observations) == 0 {
		return natProbeSummary(NATTypeUnknown, false, false, false, false, 0, "no NAT probe observations were provided")
	}

	var responses, failures int
	mappings := make(map[string]struct{})
	allMappedToLocal := true
	changedAddressResponse := false
	changedPortResponse := false

	for _, observation := range observations {
		if strings.TrimSpace(observation.ProbeError) != "" {
			failures++
		}
		if !observation.ResponseReceived {
			continue
		}
		mappedAddress := strings.TrimSpace(observation.MappedAddress)
		if mappedAddress == "" || observation.MappedPort <= 0 || observation.MappedPort > 65535 {
			failures++
			continue
		}
		responses++
		mappings[mappingKey(mappedAddress, observation.MappedPort)] = struct{}{}
		if !observation.mapsToLocalEndpoint() {
			allMappedToLocal = false
		}
		changedAddressResponse = changedAddressResponse || observation.ChangedAddressResponse
		changedPortResponse = changedPortResponse || observation.ChangedPortResponse
	}

	if responses == 0 {
		if failures > 0 {
			return natProbeSummary(NATTypeProbeFailed, false, false, false, true, 0, "NAT probe could not complete; use Relay fallback")
		}
		return natProbeSummary(NATTypeUDPBlocked, false, false, false, true, 0, "no UDP probe responses were received; use Relay fallback")
	}

	mappingStable := len(mappings) <= 1
	if !mappingStable {
		return natProbeSummary(NATTypeSymmetric, true, false, false, true, responses, "external UDP mapping changed across probe servers; prefer Relay fallback")
	}
	if allMappedToLocal && changedAddressResponse && changedPortResponse {
		return natProbeSummary(NATTypeOpenInternet, true, true, true, false, responses, "UDP endpoint is directly reachable without NAT")
	}
	if changedAddressResponse {
		return natProbeSummary(NATTypeFullCone, true, true, true, false, responses, "stable mapping accepts changed address probes; try hole punching")
	}
	if changedPortResponse {
		return natProbeSummary(NATTypeRestrictedCone, true, true, true, false, responses, "stable mapping accepts changed port probes; try hole punching")
	}
	return natProbeSummary(NATTypePortRestrictedCone, true, true, true, false, responses, "stable mapping is port restricted; try coordinated hole punching with Relay fallback ready")
}

func natProbeSummary(natType NATType, udpAvailable, mappingStable, holePunchRecommended, relayRecommended bool, successfulObservations int, reason string) NATProbeSummary {
	return NATProbeSummary{
		Type:                   natType,
		UDPAvailable:           udpAvailable,
		MappingStable:          mappingStable,
		HolePunchRecommended:   holePunchRecommended,
		RelayRecommended:       relayRecommended,
		SuccessfulObservations: successfulObservations,
		Reason:                 reason,
	}
}

func mappingKey(address string, port int) string {
	if parsed, err := netip.ParseAddr(strings.TrimSpace(address)); err == nil {
		address = parsed.String()
	}
	return strings.TrimSpace(address) + ":" + strconv.Itoa(port)
}

func (o NATProbeObservation) mapsToLocalEndpoint() bool {
	if o.LocalPort <= 0 || o.MappedPort != o.LocalPort {
		return false
	}
	local := strings.TrimSpace(o.LocalAddress)
	mapped := strings.TrimSpace(o.MappedAddress)
	if local == "" || mapped == "" {
		return false
	}
	localAddr, localErr := netip.ParseAddr(local)
	mappedAddr, mappedErr := netip.ParseAddr(mapped)
	if localErr == nil && mappedErr == nil {
		return localAddr == mappedAddr
	}
	return local == mapped
}
