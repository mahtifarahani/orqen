package contextpkt

// EstMethod names the only token estimate S5 has: no tokenizer is shipped, so there is no token budget, only bytes.
const EstMethod = "bytes_div_4"

// EstimateTokens is ceil(bytes/4).
func EstimateTokens(bytes int) int {
	if bytes <= 0 {
		return 0
	}
	return (bytes + 3) / 4
}

// Consumption describes how much of the budget one packet uses (manifest v2). ActualInputTokens is the whole model
// input of the role execution when the host reported it (nil = unknown); it is never compared with the packet as if
// they were equal.
type Consumption struct {
	PacketBytes       int     `json:"packet_bytes"`
	Cap               int     `json:"cap"`
	CriticalBytes     int     `json:"critical_bytes"`
	EstTokens         int     `json:"est_tokens"`
	EstMethod         string  `json:"est_method"`
	Tokenizer         *string `json:"tokenizer"`
	ActualInputTokens *int    `json:"actual_input_tokens"`
	HostOverhead      string  `json:"host_overhead"`
}

func NewConsumption(packetBytes, cap, criticalBytes int) Consumption {
	return Consumption{PacketBytes: packetBytes, Cap: cap, CriticalBytes: criticalBytes, EstTokens: EstimateTokens(packetBytes),
		EstMethod: EstMethod, HostOverhead: "unknown"}
}
